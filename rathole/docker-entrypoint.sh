#!/bin/sh

envsubst < /client.toml.in > /client.toml
rathole -c /client.toml
