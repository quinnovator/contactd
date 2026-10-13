package plan

import (
	"fmt"
	"strings"
)

// Block is a logical part of the prompt. Canonical is the planner's hashing
// representation, not a provider request. Offsets and sizes are estimates.
type Block struct {
	Kind                  string
	Tier, Offset, Tokens  int
	Seq, From, To         uint64
	Episode               uint32
	Hash, Chain           string
	Text, Canonical, Fate string
}

// Blocks exposes the already planned prompt without changing its bytes.
func (p *PromptPlan) Blocks(pr Params) []Block {
	var out []Block
	off := 0
	span, folds := p.FoldSpan(pr)
	add := func(b Block) {
		b.Offset = off
		off += b.Tokens
		out = append(out, b)
	}
	for ti, t := range p.Tiers {
		for i, msg := range t.Msgs {
			b := Block{
				Kind: msg.Role.String(), Tier: ti, Tokens: EstimateTokens(&msg), Text: msg.Text,
				Canonical: string(Canonical(&msg)), Hash: fmt.Sprintf("%016x", messageHash(&msg)),
				Chain: fmt.Sprintf("%016x", p.Chain[ti]), Fate: "retained while this tier is unchanged",
			}
			switch TierKind(ti) {
			case TierStatic: // system and tool schema configuration

			case TierProfile:
				b.Kind = "profile"
			case TierRecall:
				b.Kind = "recall"
				b.Fate = "retained while the recall set stays warm"
			case TierTail, TierLive:
				src := p.tailSrc
				if TierKind(ti) == TierLive {
					src = p.liveSrc
					b.Fate = "joins the tail after this turn"
				}
				if i < len(src) {
					b.Seq, b.From, b.To = src[i].seq, src[i].ckptFrom, src[i].ckptTo
					if src[i].flat {
						if msg.Role.String() == "assistant" {
							b.Kind = "tool_call"
						} else {
							b.Kind = "tool_result"
						}
					}
					if src[i].ckpt != 0 {
						b.Kind, b.Seq = "checkpoint", src[i].ckpt
					}
				}
				if TierKind(ti) == TierTail {
					b.Fate = "retained until a future fold or guard cut"
					if folds && i < span.Msgs {
						b.Fate = "selected for the next fold"
					}
				}
				if strings.HasPrefix(msg.Text, "<now>") {
					b.Kind = "time"
					b.Fate = "replaced when the next turn is planned"
				}
			}
			if msg.ToolCall != nil {
				b.Kind = "tool_call"
				b.Text = msg.ToolCall.Name + "\n" + msg.ToolCall.Code + string(msg.ToolCall.Args)
				if msg.Text != "" {
					b.Text = msg.Text + "\n" + b.Text
				}
			}
			if msg.ToolResult != nil {
				b.Kind, b.Text = "tool_result", msg.ToolResult.Output
			}
			add(b)
			if TierKind(ti) == TierStatic && i == 0 && len(p.Tools) > 0 {
				names := make([]string, 0, len(p.Tools))
				for _, tool := range p.Tools {
					names = append(names, tool.Name)
				}
				raw := string(CanonicalTools(p.Tools))
				add(Block{
					Kind: "tools", Tier: ti, Tokens: toolsTokens(p.Tools), Text: strings.Join(names, " "), Canonical: raw,
					Hash: fmt.Sprintf("%016x", toolsHash(p.Tools)), Chain: b.Chain, Fate: "retained until tool schemas change",
				})
			}
		}
	}
	return out
}
