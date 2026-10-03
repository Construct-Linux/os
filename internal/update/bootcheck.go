package update

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
)

// BootComplete is the target a good boot reaches: on the desktop, GDM up (the image's
// boot-complete.target.d).
const BootComplete = "boot-complete.target"

// ErrNothingToFallBackTo is a boot that did not reach BootComplete of a kernel image systemd-boot
// is not counting: a blessed one, which a reboot would only start again.
var ErrNothingToFallBackTo = errors.New("update: boot-complete.target was not reached, and systemd-boot is not counting this kernel image's tries: there is nothing to fall back to")

// BootCheck is construct-boot-check.service, once boot-complete.target was reached or failed to
// be. Boot counting falls back only from a boot that ends: one that comes up without a login screen
// stays up, never blessed and never tried again. So a boot that did not reach it, of a kernel image
// systemd-boot is counting (LoaderBootCountPath), is rebooted: systemd-boot starts the image with a
// try less, and once its tries ran out, the one before it.
func BootCheck(ctx context.Context, s System, vars Vars, log *slog.Logger) error {
	if good, err := s.Active(ctx, BootComplete); err != nil || good {
		return err
	}
	path, err := vars.String("LoaderBootCountPath", Loader)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNothingToFallBackTo
	}
	if err != nil {
		return err
	}
	log.Warn("boot-complete.target was not reached: rebooting, for systemd-boot to try this kernel image again or fall back from it", "image", path)
	return s.Reboot(ctx)
}
