#!/bin/bash
# Script to capture relevant system logs for BYOH agent diagnostics
dmesg | grep -i -E 'error|denied|permission|segfault' | tail -n 20
