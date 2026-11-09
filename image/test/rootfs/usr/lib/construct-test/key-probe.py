#!/usr/bin/python3
# A GTK 4 text field for cmd/image test's shortcuts matrix: it reports, in its window title, the
# keys that reached it (keyval and modifiers, as GTK saw them after keyd and Mutter) and what the
# field holds, as JSON the harness reads through the shell. F9 resets it: "abc" with the cursor
# at the end and an empty log. It quits after SECONDS.
import json
import sys

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Gdk", "4.0")
from gi.repository import Gdk, GLib, Gtk  # noqa: E402

SECONDS = 900
MODS = [
    (Gdk.ModifierType.CONTROL_MASK, "C-"),
    (Gdk.ModifierType.SHIFT_MASK, "S-"),
    (Gdk.ModifierType.ALT_MASK, "A-"),
    (Gdk.ModifierType.SUPER_MASK, "M-"),
]
LONE = {"Control_L", "Control_R", "Shift_L", "Shift_R", "Alt_L", "Alt_R", "Super_L", "Super_R", "Meta_L", "Meta_R"}

app = Gtk.Application(application_id="io.github.construct_linux.KeyProbe")


def activate(app):
    entry = Gtk.Entry()
    win = Gtk.ApplicationWindow(application=app, child=entry, default_width=600)
    log = []

    def show(*_):
        win.set_title(json.dumps({"probe": "gtk", "v": entry.get_text(), "k": log}))

    def reset():
        log.clear()
        entry.set_text("abc")
        entry.grab_focus()
        entry.set_position(-1)
        show()

    def pressed(_ctl, keyval, _code, state):
        name = Gdk.keyval_name(keyval) or str(keyval)
        if name == "F9":
            reset()
            return True
        if name not in LONE:
            log.append("".join(p for m, p in MODS if state & m) + name)
            show()
        return False

    keys = Gtk.EventControllerKey()
    keys.set_propagation_phase(Gtk.PropagationPhase.CAPTURE)
    keys.connect("key-pressed", pressed)
    win.add_controller(keys)
    entry.connect("changed", show)
    reset()
    win.present()
    GLib.timeout_add_seconds(SECONDS, lambda: app.quit())


app.connect("activate", activate)
sys.exit(app.run([]))
