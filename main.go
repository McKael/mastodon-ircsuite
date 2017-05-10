package main

import (
	"context"
	"net"

	"github.com/Xe/ln"
	"github.com/caarlos0/env"
	"gopkg.in/irc.v1"
)

type Config struct {
	ServerName string `env:"SERVER_NAME" envDefault:"to.ot"`
	ServerAddr string `env:"SERVER_ADDR" envDefault:"127.0.0.1:42069"`
	// XXX RIP HACK TODO fix once user accounts exist
	UserPassword string `env:"USER_PASSWORD" envDefault:"hunter2"`

	MastodonInstance     string `env:"MASTODON_INSTANCE,required"`
	MastodonToken        string `env:"MASTODON_TOKEN,required"`
	MastodonClientID     string `env:"MASTODON_CLIENT_ID,required"`
	MastodonClientSecret string `env:"MASTODON_CLIENT_SECRET,required"`
}

func main() {
	cfg := Config{}
	err := env.Parse(&cfg)
	if err != nil {
		ln.Fatal(ln.F{"err": err})
	}

	l, err := net.Listen(cfg.ServerAddr)
	if err != nil {
		ln.Fatal(ln.F{"err": err, "addr": cfg.ServerAddr})
	}

	for {
		ctx := context.Context()
		conn, err := l.Accept()
		if err != nil {
			ln.Error(err, ln.F{"addr": cfg.ServerAddr})
		}

		ir := irc.NewReader(conn)
		iw := irc.NewWriter(conn)

		msg, err := ir.ReadMessage()
		if err != nil {
			ln.Error(err, ln.F{"client_addr": conn.RemoteAddr().String()})
			conn.Close()
			continue
		}

		if msg.Command != "PASS" {
			ln.Log(ln.F{"action": "auth_failed", "client_addr": conn.RemoteAddr().String()})
			iw.Writef(":%s ERROR :authentication failed", cfg.ServerName)
			conn.Close()
			continue
		}

		if msg.Params[0] != cfg.UserPassword {
			ln.Log(ln.F{"action": "wrong_password", "client_addr": conn.RemoteAddr().String()})
			iw.Writef(":%s ERROR :authentication failed", cfg.ServerName)
			conn.Close()
			continue
		}

		mc, err := madon.RestoreApp("ircsuite", cfg.MastodonInstance, cfg.MastodonClientID, cfg.MastodonClientSecret, &madon.UserToken{AccessToken: cfg.MastodonToken})
		if err != nil {
			ln.Error(ln.F{"action": "madon.RestoreApp"})
			iw.Writef(":%s ERROR :authentication failed", cfg.ServerName)
			conn.Close()
			continue
		}
		_ = mc

		s := &Server{
			channels: map[string]struct{}{},
			mc:       mc,

			iw: iw,
			ir: ir,
		}

		go s.HandleConn(ctx, conn)
	}
}

type Server struct {
	sync.Mutex // locked at per line execution

	channels map[string]struct{} // channels the client has joined
	mc       *madon.Client

	iw *irc.Writer
	ir *irc.Reader
}
