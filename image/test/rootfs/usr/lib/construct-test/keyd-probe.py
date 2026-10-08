#!/usr/bin/python3
# A text field for cmd/image test's keyd check: the harness presses Super with V, A, C, X as a
# person does (QMP, through the kernel, keyd and the compositor) and reads what the field holds
# when this quits, after SECONDS: written to OUT, the window titled TITLE for the harness to focus.
import os
import sys

import gi

gi.require_version("Gtk", "4.0")
from gi.repository import GLib, Gtk  # noqa: E402

TITLE = "construct-keyd-probe"
OUT = "/run/user/1000/construct-test/keyd.txt"
SECONDS = 40

app = Gtk.Application(application_id="io.github.construct_linux.KeydProbe")


def activate(app):
    entry = Gtk.Entry()
    win = Gtk.ApplicationWindow(application=app, title=TITLE, child=entry, default_width=400)
    win.present()
    entry.grab_focus()

    def done():
        os.makedirs(os.path.dirname(OUT), exist_ok=True)
        with open(OUT, "w") as f:
            f.write(entry.get_text())
        app.quit()

    GLib.timeout_add_seconds(SECONDS, done)


app.connect("activate", activate)
sys.exit(app.run([]))
