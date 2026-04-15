#!/usr/bin/env bash
# calibrate_params/recalculate.sh
# 纯 bash 实现，无 LLM 依赖，供同步调用（checksum 不一致时）和定时任务两条路径复用。
# 用法：bash recalculate.sh <workspace_dir>

set -euo pipefail

WORKSPACE_DIR="${1:-$(pwd)}"
PERSONA_FILE="$WORKSPACE_DIR/memory/persona.md"
PARAMS_FILE="$WORKSPACE_DIR/character_params.yaml"
MEMORY_LOCK="$WORKSPACE_DIR/.memory.lock"

# ── 0. 检测 multi-form 模式 ───────────────────────────────────────────
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"
ACTIVE_FORM=$(grep '^active_form:' "$MEMORY_FILE" 2>/dev/null | awk '{print $2}' | tr -d '"' || true)

if [[ -n "$ACTIVE_FORM" ]]; then
    # Multi-form 模式：用 Python3 从 personality_dims_by_form 提取对应形态的 dims
    _get_dim() {
        local KEY="$1"
        python3 - "$PERSONA_FILE" "$ACTIVE_FORM" "$KEY" 2>/dev/null <<'PYEOF'
import sys
fname, form, key = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    lines = open(fname, encoding='utf-8').readlines()
except:
    print(3); sys.exit(0)
in_section = in_form = False
for line in lines:
    if line.startswith('personality_dims_by_form:'):
        in_section = True; continue
    if in_section and line.rstrip().endswith(form + ':') and line.startswith('  ') and not line.startswith('    '):
        in_form = True; continue
    if in_form:
        stripped = line.strip()
        if stripped.startswith(key + ':'):
            val = stripped.split(':', 1)[1].split('#')[0].strip()
            try: print(max(1, min(5, int(val))))
            except: print(3)
            sys.exit(0)
        if line.startswith('  ') and not line.startswith('    ') and stripped and not stripped.startswith('#'):
            break
print(3)
PYEOF
    }
else
    # 单形态模式：直接 grep 带两个空格缩进的键
    _get_dim() {
        local KEY=$1
        local VAL
        VAL=$(grep "^  ${KEY}:" "$PERSONA_FILE" 2>/dev/null \
            | awk '{print $2}' | grep -oP '^\d+' | head -1)
        # 显式空值检查：缺失维度默认 3（中性），不走算术兜底（会变成 1）
        if [[ -z "$VAL" ]]; then
            echo 3; return
        fi
        VAL=$(( VAL + 0 ))
        [[ $VAL -lt 1 ]] && VAL=1
        [[ $VAL -gt 5 ]] && VAL=5
        echo $VAL
    }
fi

EX=$(_get_dim extraversion)
EM=$(_get_dim empathy)
IN=$(_get_dim initiative)
VB=$(_get_dim verbosity)
ST=$(_get_dim stability)
OP=$(_get_dim openness)

# ── 2. checksum（对 personality_dims 各键行做 sha256，取前8位）────────
# 取所有以 2 个空格开头后接已知维度键的行（顺序固定，避免注释变化影响 checksum）
CURRENT_CHECKSUM=$(
    { grep '^active_form:' "$MEMORY_FILE" 2>/dev/null || true
      if [[ -n "$ACTIVE_FORM" ]]; then
          # multi-form: hash active form 的 dims
          python3 -c "
import sys
fname, form = '$PERSONA_FILE', '$ACTIVE_FORM'
try: lines = open(fname, encoding='utf-8').readlines()
except: sys.exit(0)
in_s = in_f = False
for l in lines:
    if l.startswith('personality_dims_by_form:'): in_s=True; continue
    if in_s and l.rstrip().endswith(form+':') and l.startswith('  ') and not l.startswith('    '): in_f=True; continue
    if in_f:
        s=l.strip()
        if any(s.startswith(k+':') for k in ['extraversion','empathy','initiative','verbosity','stability','openness']): print(s)
        elif l.startswith('  ') and not l.startswith('    ') and s and not s.startswith('#'): break
" 2>/dev/null
      else
          grep -E "^  (extraversion|empathy|initiative|verbosity|stability|openness):" "$PERSONA_FILE" 2>/dev/null
      fi
    } | sha256sum 2>/dev/null | cut -c1-8)
STORED_CHECKSUM=""
if [[ -f "$PARAMS_FILE" ]]; then
    STORED_CHECKSUM=$(grep '^persona_checksum:' "$PARAMS_FILE" \
        | awk '{print $2}' | tr -d '"' | head -1) || true
fi

# 幂等检查：checksum 相同且文件存在则跳过
if [[ -n "$CURRENT_CHECKSUM" ]] && \
   [[ "$CURRENT_CHECKSUM" == "$STORED_CHECKSUM" ]] && \
   [[ -f "$PARAMS_FILE" ]]; then
    exit 0
fi

# ── 3. 计算权重（MIN_FLOOR=8 保护）──────────────────────────────
_max() { echo $(( $1 > $2 ? $1 : $2 )); }
_clamp() {
    local V=$1 LO=$2 HI=$3
    [[ $V -lt $LO ]] && V=$LO
    [[ $V -gt $HI ]] && V=$HI
    echo $V
}

LISTEN_RAW=$(( 30 + EM*2 - EX ))
SHARE_RAW=$(( 15 + EX*2 + OP*2 - (6 - IN) ))
OBSERVE_RAW=$(( 25 + ST - EM ))
SILENCE_RAW=$(( 20 - EX*2 + (6 - VB)*2 ))

LISTEN=$(_max $LISTEN_RAW 8)
SHARE=$(_max $SHARE_RAW 8)
OBSERVE=$(_max $OBSERVE_RAW 8)
SILENCE=$(_max $SILENCE_RAW 8)

# 归一化到总和=100
TOTAL=$(( LISTEN + SHARE + OBSERVE + SILENCE ))
L=$(( LISTEN  * 100 / TOTAL ))
S=$(( SHARE   * 100 / TOTAL ))
O=$(( OBSERVE * 100 / TOTAL ))
SIL=$(( 100 - L - S - O ))   # 修正舍入误差，统一给 SILENCE

# ── 4. 计算其他参数 ───────────────────────────────────────────
BASE_PROB=$(( 16 + IN * 3 ))
MAX_SKIP=$(( 14 - IN * 3 ))
[[ $MAX_SKIP -lt 2 ]] && MAX_SKIP=2

Q_INT=$(( 2 + (EM - 1) / 2 ))

GEN_DAY=$(( 40 + (VB - 3) * 10 ))
GEN_DAY=$(_clamp $GEN_DAY 20 80)
GEN_NIGHT=$(( 10 + (VB - 1) * 3 ))
LOG_LEN=$(( 100 + VB * 40 ))

# ── 5. 写入 character_params.yaml（flock 加锁）──────────────────
NOW_TS=$(date -Iseconds 2>/dev/null || date +%Y-%m-%dT%H:%M:%S)

exec 9>"$MEMORY_LOCK"
flock -x 9

# 原子写入：先写临时文件再 mv，防止进程被杀时留下半写文件
TMP_FILE=$(mktemp "${PARAMS_FILE}.tmp.XXXXXX")
cat > "$TMP_FILE" << EOF
# 自动生成，勿手动修改
# 由 calibrate_params/recalculate.sh 基于 persona.md 的 personality_dims 计算
generated_at: "${NOW_TS}"
persona_checksum: "${CURRENT_CHECKSUM}"

response_mode_weights:
  LISTEN: ${L}
  SHARE: ${S}
  OBSERVE: ${O}
  SILENCE: ${SIL}

proactive:
  base_prob: ${BASE_PROB}
  max_skip: ${MAX_SKIP}

conversation:
  question_interval: ${Q_INT}
  max_questions_per_session: 8

life_sim:
  gen_threshold_day: ${GEN_DAY}
  gen_threshold_night: ${GEN_NIGHT}
  log_max_length: ${LOG_LEN}
EOF

mv "$TMP_FILE" "$PARAMS_FILE"
flock -u 9
exec 9>&-
