#!/usr/bin/python3
# Walks the graphical installer's pages as a person does, in the live session (cmd/image test):
# the disk, the person - their login suggested, Next only for a complete form - the security key,
# none plugged in, the confirmation, and install() up to starting construct, which fails here on
# purpose, then the done page. A callback that raises fails the walk: GTK would only print it.
#
# The window is shown at its own size, and on the installing and done pages what a person must
# read is checked to be on the screen without scrolling: the step (which security key to touch,
# of two), the bar, the recovery key and Restart. A laptop once showed the bar without the step,
# and a note that the recovery key is not shown again with the key scrolled out of sight. Each of
# the two pages is saved to SHOTS for the report (cmd/image test copies them out).
import importlib.machinery
import importlib.util
import json
import os
import sys
import time
import traceback

SHOTS = "/run/user/1000/construct-test"

raised = []


# A callback's exception is kept to fail the walk at its end; one that stops the walk is printed too,
# for the report to say what stopped it.
def hook(*exc):
    raised.append("".join(traceback.format_exception(*exc)))
    sys.stderr.write(raised[-1])


sys.excepthook = hook

loader = importlib.machinery.SourceFileLoader("installer", "/usr/bin/construct-installer")
spec = importlib.util.spec_from_loader("installer", loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)
from gi.repository import Adw, Gio, GLib, Gtk  # noqa: E402

Adw.init()
# The live session's own installer holds the application's name: this one runs beside it.
app = m.App()
app.set_flags(Gio.ApplicationFlags.NON_UNIQUE)
app.register(None)
w = m.Installer(app)
# A VM with the stick alone has no disk to install on: one stands in, as construct disks lists them.
if w.disk is None:
    w.disk = {"path": "/dev/nvme0n1", "model": "Test disk", "size": 64 << 30, "serial": "",
              "removable": False, "usb": False, "default": True}
    w.disks = [w.disk]
w.nav.push(w.disk_page())
w.nav.push(w.person_page())
w.name.set_text("Ada Lovelace")
w.password.set_text("a b c")
w.again.set_text("a b c")
w.on_name()
w.check_person()
assert w.login.get_text() == "ada", "login suggested: %r" % w.login.get_text()
assert w.person_next.get_sensitive(), "Next off: %r" % w.person_hint.get_text()
w.again.set_text("other")
w.check_person()
assert not w.person_next.get_sensitive() and w.person_hint.get_text(), "Next on with passwords apart"
w.again.set_text("a b c")
w.check_person()
# No key is plugged into the VM: the page says so and Next goes on without one.
w.nav.push(w.key_page())
assert w.chosen_keys() == [], w.chosen_keys()
w.nav.push(w.confirm_page())
w.present()


def settle(seconds=1.5):
    """Runs GTK's loop until the window is laid out and a page pushed has finished sliding in."""
    end = time.monotonic() + seconds
    while time.monotonic() < end or not w.get_mapped():
        GLib.MainContext.default().iteration(False)
        time.sleep(0.01)


def on_screen(widget, what):
    """Fails unless widget is drawn whole inside the window and every scrolled view around it."""
    assert widget.get_mapped(), "%s is not shown" % what
    frames, parent = [w], widget.get_parent()
    while parent is not None:
        if isinstance(parent, Gtk.ScrolledWindow):
            frames.append(parent)
        parent = parent.get_parent()
    for frame in frames:
        ok, r = widget.compute_bounds(frame)
        assert ok, "%s has no bounds in %s" % (what, type(frame).__name__)
        x, y, wd, ht = r.get_x(), r.get_y(), r.get_width(), r.get_height()
        assert x >= -0.5 and y >= -0.5 and x + wd <= frame.get_width() + 0.5 and y + ht <= frame.get_height() + 0.5, (
            "%s at %.0f,%.0f %.0fx%.0f is outside its %s, %dx%d" % (what, x, y, wd, ht, type(frame).__name__,
                                                                    frame.get_width(), frame.get_height()))


def find(widget, match):
    child = widget.get_first_child()
    while child is not None:
        if match(child):
            return child
        found = find(child, match)
        if found is not None:
            return found
        child = child.get_next_sibling()
    return None


# The window whole: its content alone is drawn on nothing, and the theme's light text on a PNG's
# transparency cannot be read.
def shot(name):
    snap = Gtk.Snapshot()
    Gtk.WidgetPaintable.new(w).snapshot(snap, w.get_width(), w.get_height())
    os.makedirs(SHOTS, exist_ok=True)
    w.get_renderer().render_texture(snap.to_node(), None).save_to_png(os.path.join(SHOTS, name + ".png"))


settle()


# Two keys of one model, as a person with a backup key has: the key page's rows, by their title.
class KeyRow:
    def __init__(self, title):
        self.title = title

    def get_title(self):
        return self.title

    def get_active(self):
        return True

    def get_text(self):
        return "1234"


w.keys = [("/dev/hidraw4", KeyRow("Yubico YubiKey OTP+FIDO+CCID"), KeyRow("")),
          ("/dev/hidraw7", KeyRow("Yubico YubiKey OTP+FIDO+CCID"), KeyRow(""))]


# construct, as the installing page reads it: the first key's touch, missed once and asked again.
class NoCLI:
    @staticmethod
    def new(*args):
        w.on_record(json.dumps({"msg": "progress", "file": "x.root.raw.xz", "percent": 100}))
        w.on_record(json.dumps({"msg": "touch the security key", "key": 1, "of": 2,
                                "device": "/dev/hidraw4", "try": 2}))
        settle()
        step = w.step.get_label()
        assert "key 1 of 2" in step and "not touched" in step, "the step says %r" % step
        on_screen(w.step, "the installing step")
        on_screen(w.progress, "the progress bar")
        shot("installer-installing")
        raise GLib.Error("not in a test")


m.Gio.Subprocess = NoCLI
w.install()
assert w.nav.get_visible_page().get_tag() == "error", w.nav.get_visible_page().get_tag()
w.recovery_key = "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ12-3456"
w.done()
assert w.nav.get_visible_page().get_tag() == "done"
settle()
on_screen(find(w, lambda c: isinstance(c, Gtk.Label) and c.get_label() == w.recovery_key), "the recovery key")
on_screen(find(w, lambda c: isinstance(c, Gtk.Button) and c.get_label() == m._("Restart")), "Restart")
shot("installer-done")
w.destroy()
if raised:
    sys.exit("".join(raised))
print("walked")
