# ~/.local/bin on a login shell's PATH, last, as usr/lib/environment.d/10-construct.conf puts it on
# the session's: sshd and login set a PATH of their own, and a shell in the session already has it.
case ":$PATH:" in
	*":$HOME/.local/bin:"*) ;;
	*) PATH="$PATH:$HOME/.local/bin" ;;
esac
