// SPEC: _spec/internal/sbx/host-browser-cdp.puml
package sandbox

import (
	"context"
	"fmt"
)

func checkGuestCDP(ctx context.Context, name, port string) error {
	script := `
if [ "${PROVEO_HOST_CDP_PORT:-}" != "$1" ]; then
	printf '%s\n' "sandbox was started with a different Chrome setting. Recreate the sandbox to apply it." >&2
	exit 1
fi
if ! command -v proveo-host-cdp >/dev/null 2>&1; then
	printf '%s\n' "rebuild or update the harness image to use host Chrome." >&2
	exit 1
fi
exec proveo-host-cdp probe`
	out, err := guestRunIn(ctx, "exec", "-w", "/", name, "--", "sh", "-c", script, "proveo-host-cdp", port)
	if err != nil {
		return fmt.Errorf("host Chrome from sandbox %s: %s", name, failure(err, out))
	}
	return nil
}
