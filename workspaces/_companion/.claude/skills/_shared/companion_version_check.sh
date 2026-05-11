#!/usr/bin/env bash
# v2.2 · 陪伴空间版本对齐检查
# 比较 _companion 模板 VERSION 与空间 VERSION，输出需手动迁移项列表

set -euo pipefail

WORKSPACE_DIR="${1:-$(pwd)}"
TEMPLATE_DIR="${TEMPLATE_DIR:-/root/cc_workspace_bot/workspaces/_companion}"

WS_NAME=$(basename "$WORKSPACE_DIR")
TPL_VERSION=$(cat "$TEMPLATE_DIR/VERSION" 2>/dev/null || echo "unknown")
WS_VERSION=$(cat "$WORKSPACE_DIR/VERSION" 2>/dev/null || echo "1.0.0")

echo "=== companion_version_check: $WS_NAME ==="
echo "模板 VERSION: $TPL_VERSION"
echo "空间 VERSION: $WS_VERSION"

if [[ "$WS_VERSION" == "$TPL_VERSION" ]]; then
    echo "[OK] 版本一致"
    exit 0
fi

echo
echo "[WARN] 版本不一致，建议迁移项："

# 检查关键机制文件是否存在
missing_items=()

check_file() {
    local relative="$1"
    local desc="$2"
    if [[ ! -f "$WORKSPACE_DIR/$relative" ]]; then
        missing_items+=("- $desc: $relative")
    fi
}

# M14 voice_whitelist
check_file "memory/voice_whitelist.yaml" "M14 声音白名单"
# M11 trigger_words
check_file "memory/trigger_words.yaml" "M11 触发词共现规则"
# M6 slop_blacklist
check_file "memory/slop_blacklist.yaml" "M6 slop 词表"
# M13 user_preferences
check_file "memory/user_preferences.md" "M13 用户偏好"
# Phase 3a log
# M1 shape_constraint
check_file ".claude/skills/_shared/shape_constraint.py" "M1 形状约束 hook"
# M4 resonance_lookup
check_file ".claude/skills/_shared/resonance_lookup.py" "M4 共振查找 hook"
# M7 silent_cot
check_file ".claude/skills/_shared/silent_cot.py" "M7 Silent CoT"
# M8 anti_example
check_file ".claude/skills/_shared/anti_example.py" "M8 反例注入"
# M6 anti_template_detector
check_file ".claude/skills/_shared/anti_template_detector.py" "M6 反模板检测"
# M11 trigger_check
check_file ".claude/skills/_shared/trigger_check.py" "M11 trigger 共现检查"
# M13 preference_loader
check_file ".claude/skills/_shared/preference_loader.py" "M13 偏好加载"
# M6 opener_blacklist
check_file ".claude/skills/_shared/opener_blacklist.py" "动态 opener blacklist"
# M3 conversation_summary
check_file ".claude/skills/conversation_summary/SKILL.md" "M3 对话历程摘要 skill"
# Phase 0 shared modules
check_file ".claude/skills/_shared/mood_classifier.py" "mood 分类器"
check_file ".claude/skills/_shared/generate_voice_whitelist.py" "voice_whitelist 生成脚本"
check_file ".claude/skills/_shared/skeleton_lint.py" "骨架 lint"

if [[ ${#missing_items[@]} -eq 0 ]]; then
    echo "  (无缺失文件；仅 VERSION 号未更新，建议手动改 $WORKSPACE_DIR/VERSION 为 $TPL_VERSION)"
else
    printf '%s\n' "${missing_items[@]}"
    echo
    echo "迁移命令（参考）："
    echo "  # 补缺失的 shared hooks（从模板复制）"
    echo "  for f in shape_constraint resonance_lookup silent_cot anti_example anti_template_detector trigger_check preference_loader opener_blacklist mood_classifier generate_voice_whitelist skeleton_lint; do"
    echo "      cp $TEMPLATE_DIR/.claude/skills/_shared/\$f.py $WORKSPACE_DIR/.claude/skills/_shared/"
    echo "  done"
    echo "  cp -r $TEMPLATE_DIR/.claude/skills/conversation_summary $WORKSPACE_DIR/.claude/skills/"
    echo "  cp $TEMPLATE_DIR/memory/slop_blacklist.yaml $WORKSPACE_DIR/memory/"
    echo "  cp $TEMPLATE_DIR/memory/trigger_words.yaml $WORKSPACE_DIR/memory/"
    echo "  cp $TEMPLATE_DIR/memory/user_preferences.md $WORKSPACE_DIR/memory/"
    echo "  # 生成 voice_whitelist"
    echo "  python3 $WORKSPACE_DIR/.claude/skills/_shared/generate_voice_whitelist.py $WORKSPACE_DIR"
fi

echo
echo "迁移完成后：echo '$TPL_VERSION' > $WORKSPACE_DIR/VERSION"
