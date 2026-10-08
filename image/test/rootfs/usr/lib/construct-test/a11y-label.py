#!/usr/bin/python3
# Whether any window of the session shows a widget labelled LABEL, as the accessibility bus has
# them: how cmd/image test reads what a GTK 4 window draws - a desktop icon - from outside it. It
# prints yes or no, so that a failure to ask is not taken for an answer.
import sys

import gi

gi.require_version("Atspi", "2.0")
from gi.repository import Atspi, GLib  # noqa: E402


def shows(a, label, depth=0):
    if a is None or depth > 30:
        return False
    try:
        if a.get_name() == label:
            return True
        return any(shows(a.get_child_at_index(i), label, depth + 1) for i in range(a.get_child_count()))
    except GLib.Error:
        return False


desktop = Atspi.get_desktop(0)
found = any(shows(desktop.get_child_at_index(i), sys.argv[1]) for i in range(desktop.get_child_count()))
print("yes" if found else "no")
