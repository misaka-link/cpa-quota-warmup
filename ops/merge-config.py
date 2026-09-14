#!/usr/bin/env python3
"""Insert/replace the cpa-quota-warmup block under plugins.configs in place (same inode).

Usage: sudo ops/merge-config.py [/var/lib/cli-proxy-api/config.yaml] [--remove]
Text-level edit: other keys, comments and ordering are preserved byte-for-byte.
"""
import os
import re
import sys

BLOCK = """    cpa-quota-warmup:
      enabled: true
      time: "05:30"                    # 每天几点预热：写 "05:30"，多个写 "05:30, 10:30"，或直接写 cron "30 5,10,15,20 * * *"
      model: "auto"                    # 预热用的模型；auto = 自动选各 provider 最便宜的；也可直接写模型名，如 gpt-5.6-luna
      accounts: ["codex-*-team.json"]  # 要预热的认证文件名，支持 * 通配；写 "*" 表示全部账号
"""

path = next((a for a in sys.argv[1:] if not a.startswith('--')), '/var/lib/cli-proxy-api/config.yaml')
remove = '--remove' in sys.argv
text = open(path, encoding='utf-8').read()
existing = re.search(r'^    cpa-quota-warmup:\n(?:      .*\n|\n)*', text, re.M)
if existing:
    text = text[:existing.start()] + ('' if remove else BLOCK) + text[existing.end():]
elif not remove:
    m = re.search(r'^  configs:\n', text, re.M)
    if not m:
        sys.exit('plugins.configs not found')
    text = text[:m.end()] + BLOCK + text[m.end():]
# In-place rewrite keeps the inode so CPA's inotify watcher keeps working.
fd = os.open(path, os.O_WRONLY)
try:
    os.ftruncate(fd, 0)
    os.write(fd, text.encode('utf-8'))
    os.fsync(fd)
finally:
    os.close(fd)
print('removed' if remove else 'merged', path)
