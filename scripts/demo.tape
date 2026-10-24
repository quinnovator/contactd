# The README's captures of the terminal client. From the repository root:
#
#   vhs scripts/demo.tape
#
# then shrink the animation, which is captured at full colour:
#
#   gifsicle -O3 --lossy=80 --colors 64 -b docs/images/demo.gif
#
# Needs vhs (https://github.com/charmbracelet/vhs), gifsicle, and a model
# provider key in the environment: the reply is the model's own, so each run
# says something new.

Output docs/images/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1000
Set Height 600
Set Padding 20
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 45ms
Set Framerate 15

# A fresh data directory, so Ada starts as a stranger. The terminal the tape is
# run from must not be mistaken for the one it draws in.
Hide
Type "unset TERM_PROGRAM TMUX STY; export CONTACTD_DATA_DIR=$(mktemp -d) CONTACTD_TRY_PORT=18090; mise run try -- -mute; exit"
Enter
Wait+Screen@180s /Message ada/
Sleep 2s
Show

Sleep 1.5s
Screenshot docs/images/avatar.png
Type "hey ada! got a neat python one-liner to count the words in a file?"
Sleep 500ms
Enter
Sleep 11s
Screenshot docs/images/reply.png
Sleep 9s
