#!/usr/bin/env bash
# Build the debug APK and install it on the device adb reports.
set -euo pipefail
cd "$(dirname "$0")"

gradle --no-daemon -q :app:assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
adb shell pm list packages ca.proveo.hello
