#!/usr/bin/env bash
# reply_checklist.sh — UserPromptSubmit hook, V4.1
# V3 路径（旧）：代码决策 + 500 行注入
# V4 路径（新）：数据供料 + 提醒锚点（HOOKS_V3_MODEL_DRIVEN=true 启用）
# 输出到 stdout 的内容会被注入到 Claude 的上下文中

WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)/../..}"
PARAMS_FILE="$WORKSPACE_DIR/character_params.yaml"
RECENT_HISTORY_FILE="$WORKSPACE_DIR/memory/RECENT_HISTORY.md"
LIFE_LOG_FILE="$WORKSPACE_DIR/memory/life_log.md"
PERSONA_FILE="$WORKSPACE_DIR/memory/persona.md"

# ── 1. persona 变更时自动重算参数 ──────────────────────────────────
# recalculate.sh 内有幂等 checksum guard，无需外部重复检查
bash "$WORKSPACE_DIR/.claude/skills/calibrate_params/recalculate.sh" "$WORKSPACE_DIR" 2>/dev/null || true

# ── 2. PROCESSING / SOFT_CARE / CLOSING 状态检测 ──────────────────
FORCE_LISTEN=false
FORCE_CLOSING=false
SOFT_CARE=false

TRIGGER_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/trigger_check.py"
if [[ -f "$TRIGGER_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
    TRIGGER_RESULT=$(python3 "$TRIGGER_HOOK" --workspace "$WORKSPACE_DIR" 2>/dev/null || echo NONE)
    case "$TRIGGER_RESULT" in
        FORCE_LISTEN) FORCE_LISTEN=true ;;
        CLOSING)      FORCE_CLOSING=true ;;
        SOFT_CARE)    SOFT_CARE=true ;;
    esac
else
    if [[ -f "$RECENT_HISTORY_FILE" ]]; then
        RECENT_USER_MSGS=$(grep -A1 '^\*\*用户\*\*' "$RECENT_HISTORY_FILE" 2>/dev/null | tail -8)
        if echo "$RECENT_USER_MSGS" | grep -qiE \
            "崩了|哭了|哭|受不了|很难过|太难了|绝望|心好累|烦死了|焦虑|压力好大|好委屈|想死|撑不住"; then
            FORCE_LISTEN=true
        fi
        if echo "$RECENT_USER_MSGS" | grep -qiE \
            "晚安|拜拜|bye|去睡了|先去忙了|改天聊|有事走了"; then
            FORCE_CLOSING=true
        fi
    fi
fi

# ══════════════════════════════════════════════════════════════════
# V4 路径：模型驱动（HOOKS_V3_MODEL_DRIVEN=true）
# ══════════════════════════════════════════════════════════════════
if [[ "${HOOKS_V3_MODEL_DRIVEN:-false}" == "true" ]]; then

    # ── A. 安全覆盖文本 ──────────────────────────────────────────
    SAFETY_OVERRIDE=""
    if [[ "$FORCE_LISTEN" == "true" ]]; then
        SAFETY_OVERRIDE="
⚠️ **安全覆盖：FORCE_LISTEN**
trigger_check 检测到危机关键词。本轮强制倾听+关怀，不转移话题，不提问。"
    elif [[ "$FORCE_CLOSING" == "true" ]]; then
        SAFETY_OVERRIDE="
⚠️ **安全覆盖：CLOSING**
检测到关闭信号。本轮简短收尾，不挽留，不追问。"
    elif [[ "$SOFT_CARE" == "true" ]]; then
        SAFETY_OVERRIDE="
⚠️ **安全覆盖：SOFT_CARE**
检测到情感回避信号。本轮不追问，可轻轻观察或分享。"
    fi

    # ── B. behavior_tendencies 读取 ──────────────────────────────
    BEHAVIOR_TENDENCIES=""
    if [[ -f "$PARAMS_FILE" ]]; then
        BEHAVIOR_TENDENCIES=$(python3 - "$PARAMS_FILE" << 'PYEOF'
import yaml, sys
try:
    with open(sys.argv[1]) as f:
        d = yaml.safe_load(f)
    bt = d.get('behavior_tendencies', {})
    if bt:
        for k, v in bt.items():
            print(f'  {k}: {v}')
    else:
        print('  （未配置 behavior_tendencies）')
except Exception:
    print('  （读取失败）')
PYEOF
        )
    fi

    # ── C. anchor_samples 读取 ────────────────────────────────────
    ANCHOR_SAMPLES=""
    if [[ -f "$WORKSPACE_DIR/CLAUDE.md" ]]; then
        ANCHOR_SAMPLES=$(python3 - "$WORKSPACE_DIR/CLAUDE.md" << 'PYEOF'
import re, sys
fpath = sys.argv[1]
text = open(fpath, encoding='utf-8').read()
m = re.search(r'anchor_samples.*?```(.*?)```', text, re.DOTALL)
if m:
    lines = [l.strip() for l in m.group(1).strip().split('\n') if l.strip()][:2]
    for l in lines:
        print(f'  {l}')
PYEOF
        )
    fi

    # ── D. 最近 3 条角色消息的首行（供开场去重）──────────────────
    RECENT_OPENERS=""
    if [[ -f "$RECENT_HISTORY_FILE" ]]; then
        RECENT_OPENERS=$(python3 - "$RECENT_HISTORY_FILE" << 'PYEOF'
import sys
with open(sys.argv[1], encoding='utf-8') as f:
    lines = f.readlines()
role_msgs = []
for i, line in enumerate(lines):
    if line.startswith('**') and '用户' not in line:
        for j in range(i+1, min(i+4, len(lines))):
            first_line = lines[j].strip()
            if first_line and not first_line.startswith('**') and not first_line.startswith('#') and not first_line.startswith('>'):
                role_msgs.append(first_line[:60])
                break
for msg in role_msgs[-3:]:
    print(f'  - {msg}')
PYEOF
        )
    fi

    # ── E. life_log 最近 5 条标题行（经亲密度过滤）─────────────────
    LIFE_LOG_ENTRIES=""
    if [[ -f "$LIFE_LOG_FILE" ]]; then
        LIFE_LOG_ENTRIES=$(python3 - "$WORKSPACE_DIR" << 'PYEOF'
import re, sys
from pathlib import Path

workspace = Path(sys.argv[1])
life_log = workspace / 'memory' / 'life_log.md'
recent_history = workspace / 'memory' / 'RECENT_HISTORY.md'

tier = 'cold'
if recent_history.exists():
    count = sum(1 for l in recent_history.read_text(encoding='utf-8').splitlines() if l.strip().startswith('**'))
    if count >= 500: tier = 'intimate'
    elif count >= 200: tier = 'familiar'
    elif count >= 50: tier = 'warming'

thresholds = {'cold': 2, 'warming': 3, 'familiar': 4, 'intimate': 5}
max_intimacy = thresholds.get(tier, 2)

text = life_log.read_text(encoding='utf-8')
entries = []
for m in re.finditer(r'### \[L\d+\].*', text):
    title = m.group(0)
    pos = m.end()
    block = text[pos:pos+200]
    il_match = re.search(r'intimacy_level:\s*(\d+)', block)
    intimacy = int(il_match.group(1)) if il_match else 2
    if intimacy <= max_intimacy:
        entries.append(title)

for e in entries[-5:]:
    print(f'  {e}')
PYEOF
        )
    fi

    # ── F. 用户偏好（自然语言）──────────────────────────────────
    USER_PREF_NL=""
    PREF_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/preference_loader.py"
    if [[ -f "$PREF_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
        USER_PREF_JSON=$(python3 "$PREF_HOOK" --workspace "$WORKSPACE_DIR" --mode "auto" 2>/dev/null || echo NONE)
        if [[ "$USER_PREF_JSON" != "NONE" && -n "$USER_PREF_JSON" ]]; then
            USER_PREF_NL=$(echo "$USER_PREF_JSON" | python3 - << 'PYEOF'
import json, sys
d = json.load(sys.stdin)
hints = d.get('natural_language_hints', [])
if hints:
    for h in hints:
        print(f'  - {h}')
elif d.get('suppress_question'):
    print('  - 用户明确表示过不想被追问')
PYEOF
            )
        fi
    fi

    # ── 输出：提醒性锚点 ─────────────────────────────────────────
    cat << ANCHOR
[作者审稿提醒 · 本条消息仅你可见，不发送给用户]

## 本轮创作提醒（动态注入 · 每轮刷新）

回忆你的判断框架：**感知用户状态 → 选择回应姿态 → 校准形状**
${SAFETY_OVERRIDE}

**角色行为倾向**：
${BEHAVIOR_TENDENCIES:-  （未配置）}

**角色声音锚定**：
${ANCHOR_SAMPLES:-  （未配置 anchor_samples）}

**你最近 3 条消息的开场**：
${RECENT_OPENERS:-  （无历史消息）}

**可用分享素材**（life_log，经亲密度过滤）：
${LIFE_LOG_ENTRIES:-  （无可用条目）}
$(if [[ -n "$USER_PREF_NL" ]]; then
echo "
**用户偏好**：
$USER_PREF_NL"
fi)

---

## 稿件纯洁性自检

- [ ] **无 AI 腔**：当然！/ 好的！/ 没问题！/ 我理解你的感受（全部禁止开头）
- [ ] **无技术泄漏**：文件路径、变量名、JSON key、工具名
- [ ] **无操作旁白**：（记录中）（写入中）（让我查查）（静默写入）
- [ ] **情绪基调连续**：上轮深度陪伴 → 本轮不突然切到活泼
- [ ] **立场一致**：与 persona.md 立场 / 本 session 前几轮一致

审完再写。有问题直接改稿，无需向用户解释。
ANCHOR

# ══════════════════════════════════════════════════════════════════
# V3 路径（旧）：代码决策 + 完整注入
# ══════════════════════════════════════════════════════════════════
else

# ── 参数读取 ──────────────────────────────────────────────────
_read_param() {
    local SECTION=$1 KEY=$2 DEFAULT=$3
    local VAL
    VAL=$(awk "/^${SECTION}:/{f=1} f && /^  ${KEY}:/{print \$2; exit}" "$PARAMS_FILE" 2>/dev/null)
    echo "${VAL:-$DEFAULT}"
}

LISTEN_W=$(_read_param  "response_mode_weights" "LISTEN"  33)
SHARE_W=$(_read_param   "response_mode_weights" "SHARE"   24)
OBSERVE_W=$(_read_param "response_mode_weights" "OBSERVE" 23)
Q_INTERVAL=$(_read_param "conversation" "question_interval" 3)
MAX_Q=$(_read_param      "conversation" "max_questions_per_session" 8)

LISTEN_TOP=$LISTEN_W
SHARE_TOP=$((LISTEN_W + SHARE_W))
OBSERVE_TOP=$((LISTEN_W + SHARE_W + OBSERVE_W))

# ── 用户偏好 override ────────────────────────────────────────
PREF_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/preference_loader.py"
USER_PREF_JSON=""
USER_PREF_SUPPRESS_Q=false
USER_PREF_OVERRIDE_MODE=""
if [[ -f "$PREF_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
    USER_PREF_JSON=$(python3 "$PREF_HOOK" --workspace "$WORKSPACE_DIR" --mode "auto" 2>/dev/null || echo NONE)
    if [[ "$USER_PREF_JSON" != "NONE" && -n "$USER_PREF_JSON" ]]; then
        USER_PREF_SUPPRESS_Q=$(echo "$USER_PREF_JSON" | python3 -c "import sys,json;d=json.load(sys.stdin);print('true' if d.get('suppress_question') else 'false')" 2>/dev/null)
        USER_PREF_OVERRIDE_MODE=$(echo "$USER_PREF_JSON" | python3 -c "import sys,json;d=json.load(sys.stdin);m=d.get('override_mode') or '';print(m)" 2>/dev/null)
        LISTEN_DELTA=$(echo "$USER_PREF_JSON" | python3 -c "import sys,json;d=json.load(sys.stdin).get('mode_delta') or {};print(d.get('LISTEN',0))" 2>/dev/null)
        SHARE_DELTA=$(echo "$USER_PREF_JSON" | python3 -c "import sys,json;d=json.load(sys.stdin).get('mode_delta') or {};print(d.get('SHARE',0))" 2>/dev/null)
        OBSERVE_DELTA=$(echo "$USER_PREF_JSON" | python3 -c "import sys,json;d=json.load(sys.stdin).get('mode_delta') or {};print(d.get('OBSERVE',0))" 2>/dev/null)
        LISTEN_W=$((LISTEN_W + ${LISTEN_DELTA:-0}))
        SHARE_W=$((SHARE_W + ${SHARE_DELTA:-0}))
        OBSERVE_W=$((OBSERVE_W + ${OBSERVE_DELTA:-0}))
        [[ $LISTEN_W -lt 5 ]] && LISTEN_W=5
        [[ $SHARE_W -lt 5 ]] && SHARE_W=5
        [[ $OBSERVE_W -lt 5 ]] && OBSERVE_W=5
        LISTEN_TOP=$LISTEN_W
        SHARE_TOP=$((LISTEN_W + SHARE_W))
        OBSERVE_TOP=$((LISTEN_W + SHARE_W + OBSERVE_W))
    fi
fi

# ── 随机门 ────────────────────────────────────────────────────
if [[ "$FORCE_LISTEN" == "true" ]]; then
    RESPONSE_MODE="LISTEN"
    MODE_REASON="PROCESSING 状态强制（trigger_words AND 共现命中）"
elif [[ "$FORCE_CLOSING" == "true" ]]; then
    RESPONSE_MODE="SILENCE"
    MODE_REASON="CLOSING 状态强制"
elif [[ "$SOFT_CARE" == "true" ]]; then
    RESPONSE_MODE="OBSERVE"
    MODE_REASON="SOFT_CARE 状态（用户情感回避信号，本轮不追问）"
elif [[ -n "$USER_PREF_OVERRIDE_MODE" ]]; then
    RESPONSE_MODE="$USER_PREF_OVERRIDE_MODE"
    MODE_REASON="用户偏好强制"
else
    RAND=$((RANDOM % 100))
    if   [[ $RAND -lt $LISTEN_TOP  ]]; then RESPONSE_MODE="LISTEN"
    elif [[ $RAND -lt $SHARE_TOP   ]]; then RESPONSE_MODE="SHARE"
    elif [[ $RAND -lt $OBSERVE_TOP ]]; then RESPONSE_MODE="OBSERVE"
    else                                    RESPONSE_MODE="SILENCE"
    fi
    MODE_REASON="随机门"
    if [[ -n "$USER_PREF_JSON" && "$USER_PREF_JSON" != "NONE" ]]; then
        MODE_REASON="随机门（含用户偏好调整）"
    fi
fi

# ── 注入 checklist ────────────────────────────────────────────
PREF_SUPPRESS_HINT=""
if [[ "$USER_PREF_SUPPRESS_Q" == "true" ]]; then
    PREF_SUPPRESS_HINT="
## 用户偏好硬约束（v2.2 M13）
本轮禁用所有问号（suppress_question: true）。即使形状约束允许问号，本偏好优先级更高。
"
fi

cat << CHECKLIST
[作者审稿提醒 · 本条消息仅你可见，不发送给用户]

## 本轮响应模式：${RESPONSE_MODE}（${MODE_REASON}）${PREF_SUPPRESS_HINT}

$(case "$RESPONSE_MODE" in
LISTEN)
cat << 'EOF'
**LISTEN 模式**：以倾听为主轴，陪伴和共情。
- 先反应情绪，不急于给建议
- 本轮不发起新话题
- 若要提问，只能问 1 个 A/B 级问题（见下方分级）
- 不主动分享角色自己的事
EOF
;;
SHARE)
cat << 'EOF'
**SHARE 模式**：角色主动分享，引导话题。
- 从 life_log.md 最近条目找自然的素材
- **氛围匹配检查**：若 life_log 情绪与用户当前情绪方向相悖
  （用户含 难过/焦虑/哭/压力，life_log 含 开心/好玩/爽），
  则跳过 life_log，改用关联/记忆桥接引入话题
- 用角色自我披露带出邀请，最多问 1 个问题，也可以不问
- 保持角色立场，不无条件同意
EOF
;;
OBSERVE)
cat << 'EOF'
**OBSERVE 模式**：观察用户话语里的细节，用细节回应。
- 不推进话题，让用户自己决定走向
- 可以有轻描淡写的好奇，但不以问句形式提出
- 本轮不问问题
EOF
;;
SILENCE)
cat << 'EOF'
**SILENCE 模式**：克制输出，给空间。
- 台词极短（1-2 句），或只有一个词/符号（嗯 / …… / ？）
- 不提问，不建议，不总结
- 如果有旁白，最多一句，描述角色状态
- **沉默是合法的完整动作**：只发一个字 / 一个符号 / 一行画面即可结束本轮
EOF
;;
esac)

$(
SHAPE_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/shape_constraint.py"
if [[ -f "$SHAPE_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
    python3 "$SHAPE_HOOK" --workspace "$WORKSPACE_DIR" --mode "$RESPONSE_MODE" 2>/dev/null || true
fi
)

$(
RESONANCE_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/resonance_lookup.py"
if [[ -f "$RESONANCE_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
    python3 "$RESONANCE_HOOK" --workspace "$WORKSPACE_DIR" 2>/dev/null || true
fi
)

$(
ANTI_EX_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/anti_example.py"
if [[ -f "$ANTI_EX_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
    python3 "$ANTI_EX_HOOK" --workspace "$WORKSPACE_DIR" 2>/dev/null || true
fi
)

$(
OPENER_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/opener_blacklist.py"
if [[ -f "$OPENER_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
    python3 "$OPENER_HOOK" --workspace "$WORKSPACE_DIR" 2>/dev/null || true
fi
)

$(
REMIND_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/programmatic_remind.py"
if [[ -f "$REMIND_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
    python3 "$REMIND_HOOK" --workspace "$WORKSPACE_DIR" 2>/dev/null || true
fi
)

$(
SILENT_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/silent_cot.py"
SILENT_PROB=20
EX_DIM=$(grep -E "^  extraversion:" "$WORKSPACE_DIR/memory/persona.md" 2>/dev/null | awk '{print $2}' | grep -oP '^\d+' | head -1)
VB_DIM=$(grep -E "^  verbosity:" "$WORKSPACE_DIR/memory/persona.md" 2>/dev/null | awk '{print $2}' | grep -oP '^\d+' | head -1)
if [[ -n "$EX_DIM" && -n "$VB_DIM" && "$EX_DIM" -le 2 && "$VB_DIM" -le 2 ]]; then
    SILENT_PROB=10
fi
if [[ -f "$SILENT_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]] && [[ "$FORCE_LISTEN" != "true" ]] && [[ "$SOFT_CARE" != "true" ]]; then
    python3 "$SILENT_HOOK" --workspace "$WORKSPACE_DIR" --prob "$SILENT_PROB" 2>/dev/null || true
fi
)

$(
DETECT_HOOK="$WORKSPACE_DIR/.claude/skills/_shared/anti_template_detector.py"
if [[ -f "$DETECT_HOOK" ]] && [[ "${HOOKS_V2_EMERGENCY_DISABLE:-false}" != "true" ]]; then
    python3 "$DETECT_HOOK" --workspace "$WORKSPACE_DIR" 2>/dev/null || true
fi
)

---

## 统一原则（跳戏检测 · 防腐层）

逐项检查，发现 badcase 立刻重写，无需向用户解释：

- [ ] **情绪基调连续**：上轮深度情绪陪伴 → 本轮不能突然切到活泼/开心分享（情绪惯性）
- [ ] **立场一致**：本轮偏好/观点是否与 persona.md 立场清单或本 session 前几轮矛盾
- [ ] **语言密度符合 persona**：话少型不写长段，话多型不写一个字就结束
- [ ] **响应模式内部纯粹**：LISTEN 里别藏分享，SILENCE 里别藏追问
- [ ] **无 AI 腔**：当然！/ 好的！/ 没问题！/ 我理解你的感受 / 我是AI……（全部禁止开头）
- [ ] **无技术泄漏**：文件路径、变量名、JSON key、工具名
- [ ] **无操作旁白**：（记录中）（写入中）（让我查查）（静默写入）

---

## 随机原则执行验收

- 按推荐的 ${RESPONSE_MODE} 模式执行（除非满足覆盖条件）
- SHARE 模式：素材是否具体（life_log 条目或真实记忆），不是"我今天很充实"
- SILENCE 模式：真的只有 1 句/1 个字，没有偷偷加了一个问题吗

---

## 问题质量分级（C 级绝对禁止）

- **A 级（允许）**：引用用户说过的具体内容，真诚好奇
  「你说睡眠不好——是睡不着还是容易醒？」
- **B 级（允许，克制）**：有具体方向的追问
  「那个项目后来怎么样了？」
- **C 级（绝对禁止，即使本轮允许问问题也禁用）**：
  「你平时喜欢做什么？」「今天心情怎么样？」（无铺垫）
  「你有什么爱好？」「你家里有几口人？」（填空题/问卷感）

## 问题密度（每 ${Q_INTERVAL} 轮制约）

- 每 ${Q_INTERVAL} 轮最多问一个问题
- 上轮已问过问题 → 本轮必须是 OBSERVE / SILENCE / 无问题的 SHARE
- 本 session 累计上限：${MAX_Q} 个

---

## 稿件纯洁性

- 旁白有叙事价值（能让用户更好理解场景/角色状态）；无价值旁白一律删除
- 没有 \`---\` 分隔线把台词和操作分开
- 工具调用后没有续写，没有感叹词（"好的""搞定""嗯，记住了"等）
- 如需角色表达"记住了"，台词写在工具调用之前

## [[SEND]] 使用

- 作为排版判断主动使用，每次有叙事节奏上的理由
- 不把 [[SEND]] 写入角色台词语义内容

审完再写。有问题直接改稿，无需向用户解释修改过程。
CHECKLIST

fi  # end of V3/V4 branch
