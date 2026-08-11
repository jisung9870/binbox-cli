# Compact secret manager smoke record — v0.8.1

## Result

The secret manager rendered every entry as a compact `service / field` row in
an isolated 80x24 PTY. Three fixtures occupied three consecutive rows with no
indented field hierarchy or reserved blank detail lines.

Filtering `installed` produced exactly one visible `installed / smoke` row,
then Enter opened the unchanged action selector. Ctrl+C at the action stage
cleaned the alternate screen and exited 0. No fixture value appeared in either
selector.

Model coverage asserts that the display label is `service / field`, the
description is empty, the stable value remains the original service/field pair,
and plaintext never enters the rendered view.
