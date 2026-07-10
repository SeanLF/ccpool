#!/bin/sh
# Fake ccusage for conformance: ignore the `blocks --json` args, emit the fixture the caller points
# CCUSAGE_FIXTURE at. Lets the Go and Ruby calibration paths spawn an identical, deterministic
# "ccusage" instead of the real npx subprocess.
cat "$CCUSAGE_FIXTURE"
