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
      priority: 1
      log: true
      timezone: "Asia/Shanghai"
      base-url: "http://127.0.0.1:8317"
      api-key: ""
      message: "hi"
      max-tokens: 16
      max-rounds: 3
      catch-up-minutes: 60
      default:
        enabled: false        # 默认不预热；只对下面 auths 里显式打开的账号生效
        times: ["05:30"]
      providers:
        # Model names below are only valid where this instance's own
        # GET /v1/models actually lists them -- see README "已知限制"/宿主事实核实.
        # The plugin also prechecks each one against GET /v1/models itself
        # before sending, so a stale entry here degrades to a skip+warning
        # rather than a 404 against the account.
        antigravity: { model: "gemini-3.7-flash-high" }
        codex:       { model: "gpt-5.6-luna", reasoning-effort: "low" }
        kimi:        { model: "kimi-k2.8" }
        xai:         { model: "grok-4.6" }
        claude:      { model: "claude-haiku-4-5-20251001" }
        gemini-cli:  { model: "gemini-2.5-flash-lite" }
        aistudio:    { model: "gemini-2.5-flash-lite" }
        vertex:      { model: "gemini-2.5-flash-lite" }
      auths:
        - match: "codex-*-team.json"   # 两个 codex team 账号
          enabled: true
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
