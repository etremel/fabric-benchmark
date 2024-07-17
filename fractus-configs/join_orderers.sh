#!/usr/bin/env sh

set -eu

osnadmin channel join --channelID mychannel --config-block mychannel.block -o 128.84.139.28:9443
osnadmin channel join --channelID mychannel --config-block mychannel.block -o 128.84.139.27:9443
osnadmin channel join --channelID mychannel --config-block mychannel.block -o 128.84.139.26:9443
