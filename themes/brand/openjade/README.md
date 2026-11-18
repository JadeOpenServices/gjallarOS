# OpenJade logo

`mark.svg`, `wordmark.svg` and `lockup-leafgreen-tight.svg` are the OpenJade
logo. They are trademarks of OpenJade and are not covered by the AGPL that
covers the rest of gjallarOS. You may keep them in copies of this repository
and on gjallarOS installs. A fork or a product of your own shows its own
logo: set `gjallar.bootSplash.mark` and `gjallar.bootSplash.wordmark` to your
files (SVG or PNG), or set the wordmark to `null`, and replace
`lockup-leafgreen-tight.svg`, which the installer stick's menu draws.

The lockup is the stone and "openJade" as one drawing, trimmed to its edges.
Use it as it is; do not size the stone and the wordmark apart.

# Jade Code

`fonts/JadeCode-VF.ttf` is Jade Code, the terminal font of the OpenJade
look. It is Fira Code 6.2 with every "Fira" name changed to "Jade" and the
hinting removed; nothing else differs. It is licensed under the SIL Open Font
License 1.1, not the AGPL: `fonts/JadeCode-LICENSE.txt` says what changed and
must ship with the font. Do not rename it back to Fira.
