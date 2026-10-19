// Super with a letter, the same in every application (usr/share/construct/keys.json). keyd, below
// the compositor, swallows Super and sends a plain key no keyboard has for each letter (F13 to
// F24); this grabs those keys and sends the focused application the chord it binds for the letter:
// Ctrl+T to Files, Ctrl+Shift+T to a terminal. The chord goes through a virtual keyboard of the
// compositor's with a clean modifier state, which is why keyd must not pass Super along: a chord
// sent while Super is held reaches the application as Super+Ctrl+T, which nothing binds.
import Clutter from 'gi://Clutter';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Meta from 'gi://Meta';
import Shell from 'gi://Shell';

import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';

const TABLE = '/usr/share/construct/keys.json';

// Keysyms for what the table sends: letters are their ASCII, the rest X11's.
const KEYSYMS = {
    Control: Clutter.KEY_Control_L, Shift: Clutter.KEY_Shift_L, Alt: Clutter.KEY_Alt_L,
    Insert: Clutter.KEY_Insert, Delete: Clutter.KEY_Delete, comma: Clutter.KEY_comma,
};

function keysym(name) {
    if (name in KEYSYMS)
        return KEYSYMS[name];
    if (name.length === 1)
        return name.toLowerCase().charCodeAt(0);
    throw new Error(`keys: no keysym for ${name}`);
}

// chord takes "<Control><Shift>t" apart: the modifiers to hold, then the key.
function chord(accel) {
    const mods = [];
    let rest = accel;
    for (;;) {
        const m = rest.match(/^<(Control|Shift|Alt)>/);
        if (!m)
            break;
        mods.push(keysym(m[1]));
        rest = rest.slice(m[0].length);
    }
    return {mods, key: keysym(rest)};
}

export default class KeysExtension extends Extension {
    enable() {
        const [, bytes] = Gio.File.new_for_path(TABLE).load_contents(null);
        const table = JSON.parse(new TextDecoder().decode(bytes));
        this._defaults = new Map(table.default.map(e => [e.key, e.sends]));
        this._apps = new Map(Object.entries(table.apps).map(([id, app]) => [id, new Map(app.keys.map(e => [e.key, e.sends]))]));
        this._actions = new Map();
        for (const e of table.default) {
            const action = global.display.grab_accelerator(e.plain, Meta.KeyBindingFlags.NONE);
            if (action === Meta.KeyBindingAction.NONE) {
                console.warn(`keys: ${e.plain} could not be grabbed`);
                continue;
            }
            Main.wm.allowKeybinding(Meta.external_binding_name_for_action(action), Shell.ActionMode.NORMAL);
            this._actions.set(action, e.key);
        }
        this._activated = global.display.connect('accelerator-activated', (_display, action) => this._send(action));
        this._keyboard = Clutter.get_default_backend().get_default_seat().create_virtual_device(Clutter.InputDeviceType.KEYBOARD_DEVICE);
    }

    disable() {
        global.display.disconnect(this._activated);
        for (const action of this._actions.keys())
            global.display.ungrab_accelerator(action);
        this._actions = null;
        this._keyboard = null;
    }

    // The chord for the letter in the focused window's application, or the default's; a table
    // entry that sends nothing leaves the letter to the application (a terminal's own Ctrl+R).
    _send(action) {
        const letter = this._actions.get(action);
        if (letter === undefined)
            return;
        const window = global.display.focus_window;
        const app = window ? Shell.WindowTracker.get_default().get_window_app(window) : null;
        const forApp = this._apps.get(app?.get_id() ?? '');
        const accel = forApp?.has(letter) ? forApp.get(letter) : this._defaults.get(letter);
        if (!accel)
            return;
        const {mods, key} = chord(accel);
        const now = () => GLib.get_monotonic_time();
        for (const m of mods)
            this._keyboard.notify_keyval(now(), m, Clutter.KeyState.PRESSED);
        this._keyboard.notify_keyval(now(), key, Clutter.KeyState.PRESSED);
        this._keyboard.notify_keyval(now(), key, Clutter.KeyState.RELEASED);
        for (const m of mods.reverse())
            this._keyboard.notify_keyval(now(), m, Clutter.KeyState.RELEASED);
    }
}
