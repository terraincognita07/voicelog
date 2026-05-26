#!/usr/bin/env bash
# Download a whisper.cpp ggml model from huggingface into ./models/.
#
# Usage:
#   scripts/fetch-model.sh                        # default: ggml-small-q5_1.bin (~190MB)
#   scripts/fetch-model.sh ggml-small-q8_0.bin    # better quality, ~264MB
#   scripts/fetch-model.sh ggml-medium-q5_0.bin   # only after server RAM upgrade

set -euo pipefail

MODEL="${1:-ggml-small-q5_1.bin}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEST_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/models"
URL="https://huggingface.co/ggerganov/whisper.cpp/resolve/main/${MODEL}?download=true"

mkdir -p "$DEST_DIR"
DEST="$DEST_DIR/$MODEL"

if [ -f "$DEST" ]; then
  echo "Model already present: $DEST"
  exit 0
fi

echo "Downloading $MODEL from huggingface.co/ggerganov/whisper.cpp..."
echo "  → $DEST"
curl -L --fail --progress-bar -o "$DEST.tmp" "$URL"
mv "$DEST.tmp" "$DEST"

SIZE="$(wc -c <"$DEST" | tr -d ' ')"
echo "Saved: $DEST (${SIZE} bytes)"
