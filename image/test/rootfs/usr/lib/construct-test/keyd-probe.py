#!/usr/bin/python3
# A text field for cmd/image test's keyd check: the harness presses Super with V, A, C, X as a
# person does (QMP, through the kernel, keyd and the compositor) and reads what the field holds
# when this quits, after SECONDS: written to OUT, the window titled TITLE for the harness to focus.
import os
import sys

import gi

gi.require_version("Gtk", "4.0")
from gi.repository import Gdk, GLib, Gtk  # noqa: E402

TITLE = "construct-keyd-probe"
OUT = "/run/user/1000/construct-test/keyd.txt"
SECONDS = 40
# With an application id as the argument the probe poses as that application (the shell's keys
# extension sends a terminal Ctrl+Shift with the letter, others Ctrl), and writes the chords its
# window received, one a line - "ctrl+shift+t" - instead of the field's text.
POSE = sys.argv[1] if len(sys.argv) > 1 else ""

app = Gtk.Application(application_id=POSE or "io.github.construct_linux.KeydProbe")


def activate(app):
    entry = Gtk.Entry()
    win = Gtk.ApplicationWindow(application=app, title=TITLE, child=entry, default_width=400)
    chords = []
    if POSE:
        def pressed(_controller, keyval, _keycode, state):
            name = Gdk.keyval_name(keyval) or ""
            if name in ("Control_L", "Shift_L", "Alt_L", "Super_L", "Control_R", "Shift_R"):
                return False
            mods = [m for flag, m in ((Gdk.ModifierType.CONTROL_MASK, "ctrl"), (Gdk.ModifierType.SHIFT_MASK, "shift"),
                                      (Gdk.ModifierType.ALT_MASK, "alt"), (Gdk.ModifierType.SUPER_MASK, "super")) if state & flag]
            chords.append("+".join(mods + [name.lower()]))
            return True

        keys = Gtk.EventControllerKey(propagation_phase=Gtk.PropagationPhase.CAPTURE)
        keys.connect("key-pressed", pressed)
        win.add_controller(keys)
    win.present()
    entry.grab_focus()

    def done():
        os.makedirs(os.path.dirname(OUT), exist_ok=True)
        with open(OUT, "w") as f:
            f.write("\n".join(chords) if POSE else entry.get_text())
        app.quit()

    GLib.timeout_add_seconds(SECONDS, done)


app.connect("activate", activate)
sys.exit(app.run([]))
