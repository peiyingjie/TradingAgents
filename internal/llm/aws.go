package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"net/http"
	"time"
)

func (a *Adapter) signAWS(ctx context.Context, r *http.Request, body []byte) error {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(a.Region))
	if err != nil {
		return err
	}
	credentials, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	return v4.NewSigner().SignHTTP(ctx, credentials, r, hex.EncodeToString(digest[:]), "bedrock", a.Region, time.Now())
}
