# Hero recording for the README.  Generate the animated gif with:
#
#   vhs _spec/_assets/hero.tape                      # native (brew install vhs)
#   # or, no local install:
#   docker run --rm -v "$PWD":/vhs -w /vhs ghcr.io/charmbracelet/vhs _spec/_assets/hero.tape
#
# Prereqs: `proveo` on PATH, run from the repo root. vhs bundles ffmpeg.
# Records the capability picker on the polyglot sample (e2e/samples).

Output _spec/_assets/hero.gif

Require proveo

Set Shell "bash"
Set FontSize 20
Set Width 1040
Set Height 560
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 55ms

Hide
Type "export CURSOR_API_KEY=demo && cd e2e/samples && clear" Enter
Show

Type "proveo run cursor --scope ."
Sleep 500ms
Enter
Sleep 3s          # capability picker renders (cursor: browser + claude-in-chrome)
Up
Sleep 900ms       # move: continue -> browser
Tab
Sleep 1200ms      # Tab selects browser
Sleep 2s
Ctrl+C            # exit before the sandbox launches (demo stops at the picker)
Sleep 800ms
