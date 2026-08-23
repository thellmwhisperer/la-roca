#!/bin/sh
set -eu

input=$1
output=$2
output_dir=$(dirname "$output")
output_name=$(basename "$output")
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd)
(cd "$input" && tar -czf "$output_dir/$output_name" roca-*)
