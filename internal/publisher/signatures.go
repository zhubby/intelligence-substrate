package publisher

import (
	"context"
	"github.com/intelligence-substrate/platform/internal/desktop"
)

func verifyDesktop(ctx context.Context, p Profile, artifact string) (string, error) {
	return desktop.Verify(ctx, p.Target, p.InstallerFilename, p.ExpectedSigner, artifact)
}
