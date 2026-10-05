#!/usr/bin/python3
# Walks the graphical installer's pages as a person does, in the live session (cmd/image test):
# the disk, the person - their login suggested, Next only for a complete form - the security key,
# none plugged in, the confirmation, and install() up to starting construct, which fails here on
# purpose, then the done page. A callback that raises fails the walk: GTK would only print it.
import importlib.machinery
import importlib.util
import sys
import traceback

raised = []
sys.excepthook = lambda *exc: raised.append("".join(traceback.format_exception(*exc)))

loader = importlib.machinery.SourceFileLoader("installer", "/usr/bin/construct-installer")
spec = importlib.util.spec_from_loader("installer", loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)
from gi.repository import Adw, GLib  # noqa: E402

Adw.init()
w = m.Installer(m.App())
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


class NoCLI:
    @staticmethod
    def new(*args):
        raise GLib.Error("not in a test")


m.Gio.Subprocess = NoCLI
w.install()
assert w.nav.get_visible_page().get_tag() == "error", w.nav.get_visible_page().get_tag()
w.recovery_key = "abcd-efgh"
w.done()
assert w.nav.get_visible_page().get_tag() == "done"
if raised:
    sys.exit("".join(raised))
print("walked")
