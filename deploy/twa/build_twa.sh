#!/bin/bash
set -e
export JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64
export PATH=~/node-v22/bin:$JAVA_HOME/bin:$PATH
export ANDROID_HOME=$HOME/.bubblewrap/android-sdk
export SIGNING_KEY_PATH=/home/user/twa/agentmail.release.keystore
export SIGNING_KEY_ALIAS=agentmail
for v in BUBBLEWRAP_KEYSTORE_PASSWORD SIGNING_KEY_PATH; do
  if [ -z "$(eval echo \$$v)" ]; then echo "need $v (and optionally BUBBLEWRAP_KEY_PASSWORD / SIGNING_KEY_ALIAS)" >&2; exit 1; fi
done
cd ~/twa/app
NODE_PATH=~/twa/node_modules NODE_TLS_REJECT_UNAUTHORIZED=0 node "$(dirname "$0")/twa_noint_build.js"
ls -la *.apk
