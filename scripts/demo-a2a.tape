# The README's captures of Ada and an A2A helper. From the repository root:
#
#   vhs scripts/demo-a2a.tape
#
# then shrink the animation, which is captured at full colour. The avatars
# repaint most of the stage in every frame, so it is cut to 10 frames a second
# on one palette before gifsicle sees it:
#
#   ffmpeg -i docs/images/a2a.gif -vf "fps=10,scale=900:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=64:stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" a2a-small.gif
#   gifsicle -O3 --lossy=80 a2a-small.gif -o docs/images/a2a.gif && rm a2a-small.gif
#
# Needs vhs (https://github.com/charmbracelet/vhs), gifsicle, and a model
# provider key in the environment: the replies are the model's own, so each
# run says something new. It runs scripts/demo-a2a.sh on ports of its own, so a
# demo already open on 8080 and 9090 is left alone.

Output docs/images/a2a.gif

Set Shell "bash"
Set FontSize 14
Set Width 1200
Set Height 900
Set Padding 20
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 25ms
Set Framerate 15

# A fresh data directory, so Ada has no helpers yet. Once the helper's first
# report is in, the status page's API turns degraded, for its next check to
# find. Ada speaks at 90 characters a second rather than 38, so the clip is
# not mostly her reading aloud. The terminal the tape is run from must not be
# mistaken for the one it draws in.
Hide
Type@1ms "unset TERM_PROGRAM TMUX STY; export CONTACTD_A2A_DEMO_DATA=$(mktemp -d) CONTACTD_A2A_DEMO_PORT=18080 CONTACTD_A2A_SITE_PORT=19090; "
Type@1ms `mkdir -p $CONTACTD_A2A_DEMO_DATA/chat-config && printf '[avatar]\nrate = 90\n' >$CONTACTD_A2A_DEMO_DATA/chat-config/chat.toml; `
Type@1ms `(until curl -s localhost:18080/v1/contacts/1/helpers | grep -q '"reports":[1-9]'; do sleep 1; done; sleep 3; curl -s -XPOST 'localhost:19090/set?component=API&state=degraded') >/dev/null 2>&1 & `
Type@1ms "clear; mise run dev:build >/dev/null && go build -o bin/demosite ./cmd/demosite && bash scripts/demo-a2a.sh -mute; exit"
Enter
Wait+Screen@180s /Message ada/
Sleep 2s
Show

Sleep 1.5s
Type "have a helper watch http://127.0.0.1:19090 every 30s for half an hour. lead with a component/state table for the baseline, then speak up only when something changes"
Sleep 500ms
Enter

# Ada starts the helper over A2A, and its first report comes back.
Wait+Screen@90s /next check/
Sleep 12s

# The quiet stretch until the next check is cut.
Hide
Wait+Screen@90s /next check 0:0[1-4]/
Show

# The next check finds the change, and Ada passes it on.
Wait+Screen@90s /degraded/
Sleep 9s

# The exchanges behind it, as they went over the wire.
Type "/a2a"
Sleep 500ms
Enter
Sleep 1.5s
Up
Sleep 500ms
Enter
Sleep 2s
Screenshot docs/images/a2a-exchanges.png
Sleep 3s
