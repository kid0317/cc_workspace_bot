#!/usr/bin/env bash
# reply_checklist.sh — UserPromptSubmit hook, V3.1
# 职责：参数读取 → PROCESSING/CLOSING 检测 → 随机门注入 → 跳戏 checklist 注入
# 输出到 stdout 的内容会被注入到 Claude 的上下文中

WORKSPACE_DIR="${WORKSPACE_DIR:-$(pwd)/../..}"
PARAMS_FILE="$WORKSPACE_DIR/character_params.yaml"
RECENT_HISTORY_FILE="$WORKSPACE_DIR/memory/RECENT_HISTORY.md"

# ── 1. checksum 同步检测：persona 更新后立即重算参数 ──────────────
_ACTIVE_FORM=$(grep '^active_form:' "$WORKSPACE_DIR/memory/MEMORY.md" 2>/dev/null | awk '{print $2}')
CURRENT_CHECKSUM=$(
    { grep '^active_form:' "$WORKSPACE_DIR/memory/MEMORY.md" 2>/dev/null
      grep -E "^  {1,2}(extraversion|empathy|initiative|verbosity|stability|openness):" \
          "$WORKSPACE_DIR/memory/persona.md" 2>/dev/null
      grep -E "^    (extraversion|empathy|initiative|verbosity|stability|openness):" \
          "$WORKSPACE_DIR/memory/persona.md" 2>/dev/null
    } | sha256sum 2>/dev/null | cut -c1-8)
STORED_CHECKSUM=$(grep '^persona_checksum:' "$PARAMS_FILE" 2>/dev/null | awk '{print $2}' | tr -d '"')
if [[ -n "$CURRENT_CHECKSUM" ]] && \
   { [[ "$CURRENT_CHECKSUM" != "$STORED_CHECKSUM" ]] || [[ ! -f "$PARAMS_FILE" ]]; }; then
    bash "$WORKSPACE_DIR/.claude/skills/calibrate_params/recalculate.sh" "$WORKSPACE_DIR" 2>/dev/null
fi

# ── 2. 参数读取（awk 两层解析，带降级默认值）──────────────────────
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

# ── 3. PROCESSING / CLOSING 状态检测（规则匹配，不依赖 LLM 判断）──
FORCE_LISTEN=false
FORCE_CLOSING=false
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

# ── 4. 随机门（$RANDOM，在 LLM 读到上下文前就决定本轮模式）────────
if [[ "$FORCE_LISTEN" == "true" ]]; then
    RESPONSE_MODE="LISTEN"
    MODE_REASON="PROCESSING 状态强制"
elif [[ "$FORCE_CLOSING" == "true" ]]; then
    RESPONSE_MODE="SILENCE"
    MODE_REASON="CLOSING 状态强制"
else
    RAND=$((RANDOM % 100))
    if   [[ $RAND -lt $LISTEN_TOP  ]]; then RESPONSE_MODE="LISTEN"
    elif [[ $RAND -lt $SHARE_TOP   ]]; then RESPONSE_MODE="SHARE"
    elif [[ $RAND -lt $OBSERVE_TOP ]]; then RESPONSE_MODE="OBSERVE"
    else                                    RESPONSE_MODE="SILENCE"
    fi
    MODE_REASON="随机门"
fi

# ── 5. 注入 checklist ────────────────────────────────────────────
cat << CHECKLIST
[作者审稿提醒 · 本条消息仅你可见，不发送给用户]

## 本轮响应模式：${RESPONSE_MODE}（${MODE_REASON}）

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
EOF
;;
esac)

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
