#!/usr/bin/env bash
# Downloads the default model set the cmd/translator binary expects
# at launch:
#
#   <data>/models/vad/silero_vad.onnx
#   <data>/models/stt/zipformer-streaming-en/{encoder,decoder,joiner}.onnx
#                                            +tokens.txt
#   <data>/models/tts/piper-en-amy-low/{model.onnx, tokens.txt,
#                                        espeak-ng-data/}
#
# Total download ~375 MB. Files are unpacked into their final
# destinations; if a directory already exists the download is skipped.
#
# Usage:
#   scripts/fetch-models.sh
#
# Environment overrides:
#   DATA_ROOT      override <data> root (default: per-OS standard).
#   STT_ONLY=1     skip TTS download.
#   FORCE=1        re-download even if destination directories exist.
set -euo pipefail

if [ -n "${DATA_ROOT:-}" ]; then
  data="$DATA_ROOT"
else
  case "$(uname -s)" in
    Darwin)  data="$HOME/Library/Application Support/realtime-speech-translator" ;;
    Linux)   data="${XDG_DATA_HOME:-$HOME/.local/share}/realtime-speech-translator" ;;
    *)       echo "fetch-models: unsupported host $(uname -s)" >&2; exit 1 ;;
  esac
fi

models="$data/models"
mkdir -p "$models/stt" "$models/vad" "$models/tts"

scratch="$(mktemp -d -t rst-models-XXXXXX)"
trap 'rm -rf "$scratch"' EXIT

# ----------------------------------------------------------------------------
# Silero VAD (~640 KB) — required by the streaming engine.
# ----------------------------------------------------------------------------
vad_dst="$models/vad/silero_vad.onnx"
if [ "${FORCE:-0}" = "1" ] || [ ! -f "$vad_dst" ]; then
  echo "fetch-models: downloading silero_vad.onnx → $vad_dst"
  curl -L --fail --progress-bar \
    -o "$vad_dst" \
    https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx
else
  echo "fetch-models: skip vad (already present at $vad_dst)"
fi

# ----------------------------------------------------------------------------
# Streaming Zipformer EN (~310 MB) — the default STT model.
# ----------------------------------------------------------------------------
stt_dst="$models/stt/zipformer-streaming-en"
if [ "${FORCE:-0}" = "1" ] || [ ! -d "$stt_dst" ]; then
  echo "fetch-models: downloading sherpa-onnx-streaming-zipformer-en-2023-06-26 → $stt_dst"
  curl -L --fail --progress-bar \
    -o "$scratch/stt.tar.bz2" \
    https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-2023-06-26.tar.bz2
  tar -xjf "$scratch/stt.tar.bz2" -C "$scratch"
  src_dir="$scratch/sherpa-onnx-streaming-zipformer-en-2023-06-26"
  rm -rf "$stt_dst"
  mkdir -p "$stt_dst"
  # The tarball ships int8 + non-quantised variants; keep the
  # non-quantised .onnx as the canonical name the cmd expects.
  cp "$src_dir/encoder-epoch-99-avg-1-chunk-16-left-128.onnx"  "$stt_dst/encoder.onnx"
  cp "$src_dir/decoder-epoch-99-avg-1-chunk-16-left-128.onnx"  "$stt_dst/decoder.onnx"
  cp "$src_dir/joiner-epoch-99-avg-1-chunk-16-left-128.onnx"   "$stt_dst/joiner.onnx"
  cp "$src_dir/tokens.txt"                                     "$stt_dst/tokens.txt"
else
  echo "fetch-models: skip stt (already present at $stt_dst)"
fi

# ----------------------------------------------------------------------------
# Piper EN voice (~67 MB) — default TTS.
# ----------------------------------------------------------------------------
if [ "${STT_ONLY:-0}" != "1" ]; then
  tts_dst="$models/tts/piper-en-amy-low"
  if [ "${FORCE:-0}" = "1" ] || [ ! -d "$tts_dst" ]; then
    echo "fetch-models: downloading vits-piper-en_US-amy-low → $tts_dst"
    curl -L --fail --progress-bar \
      -o "$scratch/tts.tar.bz2" \
      https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-piper-en_US-amy-low.tar.bz2
    tar -xjf "$scratch/tts.tar.bz2" -C "$scratch"
    src_dir="$scratch/vits-piper-en_US-amy-low"
    rm -rf "$tts_dst"
    mkdir -p "$tts_dst"
    cp "$src_dir/en_US-amy-low.onnx" "$tts_dst/model.onnx"
    cp "$src_dir/tokens.txt"         "$tts_dst/tokens.txt"
    cp -R "$src_dir/espeak-ng-data"  "$tts_dst/espeak-ng-data"
  else
    echo "fetch-models: skip tts (already present at $tts_dst)"
  fi
fi

echo "fetch-models: done."
echo
echo "Update config.yaml (or the Settings tab) to point at:"
echo "  STTModel: zipformer-streaming-en"
echo "  TTSVoice: piper-en-amy-low"
