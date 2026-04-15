package commands

import (
	"context"
	"fmt"
	"strings"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type SayArgs struct {
	Text string `cmd:"help=message text to broadcast"`
}

type SayResult struct {
	Broadcast string
}

func registerSay(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "chat.broadcast",
		Capability:  "chat.broadcast",
		Description: "broadcast a server chat message to all players",
		Route:       cmdsys.RouteLocal,
		Args:        SayArgs{},
		Result:      SayResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(SayArgs)
			text := strings.TrimSpace(args.Text)
			if text == "" {
				return nil, fmt.Errorf("message text is required")
			}
			gw := resolver.AnyLocalWorld()
			if gw == nil {
				return nil, fmt.Errorf("no local game world available")
			}
			_, err := ExecOnLoop(gw, func() (any, error) {
				mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{
					Username: "[SERVER]",
					Text:     text,
				})
				return nil, nil
			})
			if err != nil {
				return nil, err
			}
			return SayResult{Broadcast: text}, nil
		},
	})
}
