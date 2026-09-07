#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if [ -x "$script_dir/vohive-plus" ]; then
  exec "$script_dir/vohive-plus" --prepare-usb
fi

if [ -x /opt/vohive/bin/vohive-plus ]; then
  exec /opt/vohive/bin/vohive-plus --prepare-usb
fi

echo '{"supported_device_found":false,"prepared":false,"message":"未找到 vohive-plus 二进制，无法执行 WSL USB 准备。"}'
exit 1
