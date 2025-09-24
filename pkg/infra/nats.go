package infra

import (
	"path/filepath"
	"time"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/nats-io/nats.go"
)

func GetNATSConnection(natsConfig config.NatsConfig, environment string) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.MaxReconnects(-1), // retry forever
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectHandler(func(nc *nats.Conn) {
			logger.Warn("Disconnected from NATS")
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			logger.Info("Reconnected to NATS", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			logger.Info("NATS connection closed!")
		}),
	}

	natsURL := natsConfig.URL
	if environment != constant.EnvProduction {
		if natsURL == "" {
			natsURL = nats.DefaultURL
		}

		opts = append(opts, nats.ErrorHandler(NatsErrHandler))
		return nats.Connect(natsURL, opts...)
	}

	// Load TLS config from configuration with sensible fallbacks
	clientCert := natsConfig.TLS.ClientCert
	clientKey := natsConfig.TLS.ClientKey
	caCert := natsConfig.TLS.CACert

	if clientCert == "" {
		clientCert = filepath.Join(".", "certs", "client-cert.pem")
	}
	if clientKey == "" {
		clientKey = filepath.Join(".", "certs", "client-key.pem")
	}
	if caCert == "" {
		caCert = filepath.Join(".", "certs", "rootCA.pem")
	}

	opts = append(opts,
		nats.ClientCert(clientCert, clientKey),
		nats.RootCAs(caCert),
		nats.UserInfo(natsConfig.Username, natsConfig.Password),
		nats.ErrorHandler(NatsErrHandler),
	)
	return nats.Connect(natsURL, opts...)
}

func NatsErrHandler(nc *nats.Conn, sub *nats.Subscription, natsErr error) {
	logger.Error("NATS Error", natsErr)
	if natsErr == nats.ErrSlowConsumer {
		pendingMsgs, _, err := sub.Pending()
		if err != nil {
			logger.Error("Error getting pending messages: ", err)
			return
		}

		logger.Error("Falling behind with pending messages on subject", "pending", pendingMsgs, "subject", sub.Subject)
	}
}
