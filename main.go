package main

import (
	"context"
	"net"
	"strings"
	"sync"

	"github.com/McKael/madon"
	"github.com/Xe/ln"
	"github.com/caarlos0/env"
	_ "github.com/joho/godotenv/autoload"
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

	l, err := net.Listen("tcp", cfg.ServerAddr)
	if err != nil {
		ln.Fatal(ln.F{"err": err, "addr": cfg.ServerAddr})
	}

	for {
		ctx := context.Background()
		conn, err := l.Accept()
		if err != nil {
			ln.Error(err, ln.F{"addr": cfg.ServerAddr})
		}

		ir := irc.NewReader(conn)
		iw := irc.NewWriter(conn)

	again:
		msg, err := ir.ReadMessage()
		if err != nil {
			ln.Error(err, ln.F{"client_addr": conn.RemoteAddr().String()})
			conn.Close()
			continue
		}

		if msg.Command != "PASS" {
			if msg.Command == "CAP" {
				goto again
			}

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
			ln.Error(err, ln.F{"action": "madon.RestoreApp"})
			iw.Writef(":%s ERROR :authentication failed", cfg.ServerName)
			conn.Close()
			continue
		}
		_ = mc

		ctx, cancel := context.WithCancel(ctx)

		s := &Server{
			channels: map[string]struct{}{},
			mc:       mc,

			cfg:    &cfg,
			cancel: cancel,
			conn:   conn,
			iw:     iw,
			ir:     ir,
		}

		go s.HandleConn(ctx)
	}
}

type Server struct {
	sync.Mutex // locked at per line execution

	cfg      *Config
	channels map[string]struct{} // channels the client has joined
	mc       *madon.Client

	cancel context.CancelFunc
	conn   net.Conn
	iw     *irc.Writer
	ir     *irc.Reader

	nickname   string
	nick       bool
	user       bool
	registered bool
}

func (s *Server) F() ln.F {
	return ln.F{
		"registered":  s.registered,
		"remote_addr": s.conn.RemoteAddr().String(),
		"nickname":    s.nickname,
	}
}

func (s *Server) HandleConn(ctx context.Context) {
	defer s.conn.Close()
	defer s.cancel()

	s.nickname = "*"

	for {
		if (s.nick && s.user) && !s.registered {
			s.iw.Writef(":%s 001 %s :Welcome to an IRC relay!", s.cfg.ServerName, s.nickname)
			s.registered = true

			err := s.stream(ctx, "&user", "user", "")
			if err != nil {
				ln.Error(err, s.F(), ln.F{"action": "user_stream"})
				return
			}

			err = s.stream(ctx, "&public", "public", "")
			if err != nil {
				ln.Error(err, s.F(), ln.F{"action": "public_stream"})
				return
			}
		}

		msg, err := s.ir.ReadMessage()
		if err != nil {
			ln.Error(err, s.F(), ln.F{"action": "public_stream"})
			return
		}

		ln.Log(s.F(), ln.F{"verb": msg.Command})

		switch msg.Command {
		case "NICK":
			s.nick = true
			s.nickname = msg.Params[0]

		case "USER":
			s.user = true
		case "MODE":
			s.iw.Writef(":%s MODE %s %s", s.cfg.ServerName, s.nickname, msg.Params[1])
		case "PING":
			msg.Host = s.cfg.ServerName
			msg.Command = "PONG"
			s.iw.WriteMessage(msg)
		case "JOIN":
			// hashtag streaming
			target := msg.Params[0]
			if target[0] != '#' {
				s.iw.Writef("%s 404 %s :Unknown hashtag", s.cfg.ServerName, s.nickname)
				continue
			}

			err = s.stream(ctx, target, "hashtag", target[1:])
			if err != nil {
				ln.Error(err, s.F(), ln.F{"action": "hashtag_stream", "hashtag": target})
			}
		default:
			s.iw.Writef(":%s 421 %s :Unknown command %q", s.cfg.ServerName, s.nickname, msg.Command)
			continue
		}
	}
}

func (s *Server) stream(ctx context.Context, chName, streamName, hashtag string) error {
	evChan := make(chan madon.StreamEvent, 10)
	stop := make(chan bool)
	done := make(chan bool)

	f := s.F()
	f["channel"] = chName
	f["stream"] = streamName
	f["hashtag"] = hashtag

	err := s.mc.StreamListener(streamName, hashtag, evChan, stop, done)
	if err != nil {
		ln.Error(err, f, ln.F{"action": "s.mc.streamListener"})
	}

	s.iw.Writef(":%s JOIN %s", s.nickname, chName)

	go func() {
		for {
			select {
			case <-ctx.Done():
				close(stop)
				return
			case <-done:
				return
			case ev := <-evChan:
				switch ev.Event {
				case "update":
					st := ev.Data.(madon.Status)
					s.iw.Writef(":%s PRIVMSG %s :%s: %s%s", streamName, chName, st.Account.Username, st.SpoilerText+" ", strings.Replace(st.Content, "\n", " ", 0))
				}
			}
		}
	}()

	return nil
}
