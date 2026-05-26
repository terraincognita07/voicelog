#!/usr/bin/env bash
# Smoke-test the local whisper-server. Downloads the canonical JFK sample WAV
# (~338 KB) on first run and POSTs it to /inference.
#
# Usage:
#   scripts/whisper-smoke.sh                            # default: http://127.0.0.1:8085/inference
#   WHISPER_URL=http://whisper:8080/inference scripts/whisper-smoke.sh

set -euo pipefail

WHISPER_URL="${WHISPER_URL:-http://127.0.0.1:8085/inference}"
SAMPLE_URL="https://github.com/ggml-org/whisper.cpp/raw/master/samples/jfk.wav"
SAMPLE_PATH="${TMPDIR:-/tmp}/voicelog-jfk.wav"

if [ ! -f "$SAMPLE_PATH" ]; then
  echo "Downloading sample WAV → $SAMPLE_PATH"
  curl -L --fail -o "$SAMPLE_PATH" "$SAMPLE_URL"
fi

echo "POST $WHISPER_URL"
curl -sS --fail "$WHISPER_URL" \
  -H "Content-Type: multipart/form-data" \
  -F file="@$SAMPLE_PATH" \
  -F response_format="json"
echo
