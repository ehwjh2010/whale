#!/bin/sh
# Smoke test: initialize → session/new → session/prompt
# Usage: DEEPSEEK_API_KEY=sk-... ./scripts/acp_smoke_test.sh [bin/whale-acp]

BIN="${1:-bin/whale-acp}"

# Auto-build if the binary doesn't exist.
if [ ! -x "$BIN" ]; then
    echo "Building $BIN..."
    go build -o "$BIN" ./cmd/whale-acp/ || exit 1
fi

TMPDIR=$(mktemp -d)
FIFO_IN="$TMPDIR/in"
FIFO_OUT="$TMPDIR/out"
mkfifo "$FIFO_IN" "$FIFO_OUT"
trap "rm -rf $TMPDIR" EXIT

# Start the ACP process first — it will block trying to open FIFO_IN for
# reading and FIFO_OUT for writing, but this is fine because we haven't
# redirected yet. We use a shell wrapper to defer the redirection.
#
# The key insight: start the process in the background connected to FIFOs,
# then open the other ends from the parent. Opening a FIFO for write blocks
# until a reader is present, and vice versa — but since we created both
# FIFOs first and the process will open both ends, we can safely open our
# end after the background process starts trying.

"$BIN" 2>/tmp/acp-test.log < "$FIFO_IN" > "$FIFO_OUT" &
PID=$!

# Now open our side. exec 3 (write to agent's stdin) and exec 4 (read from
# agent's stdout). These will block until the agent opens the other end,
# which it's already trying to do.
exec 3>"$FIFO_IN"
exec 4<"$FIFO_OUT"
sleep 0.5

# Helper: read one line from fifo
read_line() {
    read -r line <&4 || true
    echo "$line"
}

echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}' >&3
INIT=$(read_line)
echo "INIT: $INIT"

echo '{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}' >&3
SESS=$(read_line)
echo "SESSION: $SESS"

SID=$(echo "$SESS" | grep -o '"sessionId":"[^"]*"' | cut -d'"' -f4)
if [ -z "$SID" ]; then
    echo "ERROR: could not extract sessionId"
    kill $PID 2>/dev/null
    exit 1
fi
echo "SessionID: $SID"

PROMPT="{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"session/prompt\",\"params\":{\"sessionId\":\"$SID\",\"prompt\":[{\"type\":\"text\",\"text\":\"Say hello in one sentence.\"}]}}"
echo "$PROMPT" >&3

# Read until we see id:3 response
while read -r line <&4; do
    echo "OUT: $line"
    if echo "$line" | grep -q '"id":3'; then
        break
    fi
done

# Close descriptors to signal EOF.
exec 3>&-
exec 4<&-

kill $PID 2>/dev/null
wait $PID 2>/dev/null
echo "---TEST COMPLETE---"
