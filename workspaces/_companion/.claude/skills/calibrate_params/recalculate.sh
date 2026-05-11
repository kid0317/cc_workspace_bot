#!/usr/bin/env bash
# calibrate_params/recalculate.sh (v5.3)
# 纯 bash 实现，无 LLM 依赖。
# v5.3: behavior_tendencies 细粒度分支（em/op 二级区分，解决角色趋同）
# v5.2: 新增 behavior_tendencies 半结构化字段（供 V4 模型消费路径）
# v5.1: 不再 heredoc 全量重写——保留 v5.1 新增字段（emotional_range / white_space_prob 等）。
# 用法：bash recalculate.sh <workspace_dir>

set -euo pipefail

WORKSPACE_DIR="${1:-$(pwd)}"
PERSONA_FILE="$WORKSPACE_DIR/memory/persona.md"
PARAMS_FILE="$WORKSPACE_DIR/character_params.yaml"
MEMORY_LOCK="$WORKSPACE_DIR/.memory.lock"
MEMORY_FILE="$WORKSPACE_DIR/memory/MEMORY.md"

# ── 0. 检测 multi-form 模式 ───────────────────────────────────────────
ACTIVE_FORM=$(grep '^active_form:' "$MEMORY_FILE" 2>/dev/null | awk '{print $2}' | tr -d '"' || true)

if [[ -n "$ACTIVE_FORM" ]]; then
    _get_dim() {
        local KEY="$1"
        python3 - "$PERSONA_FILE" "$ACTIVE_FORM" "$KEY" 2>/dev/null <<'PYEOF'
import sys
fname, form, key = sys.argv[1], sys.argv[2], sys.argv[3]
try: lines = open(fname, encoding='utf-8').readlines()
except: print(3); sys.exit(0)
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
    _get_dim() {
        local KEY=$1 VAL
        VAL=$(grep "^  ${KEY}:" "$PERSONA_FILE" 2>/dev/null | awk '{print $2}' | grep -oP '^\d+' | head -1)
        [[ -z "$VAL" ]] && { echo 3; return; }
        VAL=$(( VAL + 0 ))
        [[ $VAL -lt 1 ]] && VAL=1
        [[ $VAL -gt 5 ]] && VAL=5
        echo $VAL
    }
fi

EX=$(_get_dim extraversion); EM=$(_get_dim empathy); IN=$(_get_dim initiative)
VB=$(_get_dim verbosity);    ST=$(_get_dim stability); OP=$(_get_dim openness)

# ── 1. checksum ────────────────────────────────────────────────────────
CURRENT_CHECKSUM=$(
    { grep '^active_form:' "$MEMORY_FILE" 2>/dev/null || true
      if [[ -n "$ACTIVE_FORM" ]]; then
          _PERSONA="$PERSONA_FILE" _FORM="$ACTIVE_FORM" python3 - << 'HASH_EOF' 2>/dev/null
import os
fname = os.environ.get('_PERSONA', '')
form  = os.environ.get('_FORM', '')
try: lines = open(fname, encoding='utf-8').readlines()
except: raise SystemExit
in_s = in_f = False
for l in lines:
    if l.startswith('personality_dims_by_form:'): in_s=True; continue
    if in_s and l.rstrip().endswith(form+':') and l.startswith('  ') and not l.startswith('    '): in_f=True; continue
    if in_f:
        s=l.strip()
        if any(s.startswith(k+':') for k in ['extraversion','empathy','initiative','verbosity','stability','openness']): print(s)
        elif l.startswith('  ') and not l.startswith('    ') and s and not s.startswith('#'): break
HASH_EOF
      else
          grep -E "^  (extraversion|empathy|initiative|verbosity|stability|openness):" "$PERSONA_FILE" 2>/dev/null
      fi
    } | sha256sum 2>/dev/null | cut -c1-8)

STORED_CHECKSUM=""
if [[ -f "$PARAMS_FILE" ]]; then
    STORED_CHECKSUM=$(grep '^persona_checksum:' "$PARAMS_FILE" | awk '{print $2}' | tr -d '"' | head -1) || true
fi

if [[ -n "$CURRENT_CHECKSUM" && "$CURRENT_CHECKSUM" == "$STORED_CHECKSUM" && -f "$PARAMS_FILE" ]]; then
    exit 0
fi

# ── 2. 计算权重（MIN_FLOOR=8 保护）───────────────────────────────────
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

LISTEN=$(_max $LISTEN_RAW 8); SHARE=$(_max $SHARE_RAW 8)
OBSERVE=$(_max $OBSERVE_RAW 8); SILENCE=$(_max $SILENCE_RAW 8)

TOTAL=$(( LISTEN + SHARE + OBSERVE + SILENCE ))
L=$(( LISTEN  * 100 / TOTAL ))
S=$(( SHARE   * 100 / TOTAL ))
O=$(( OBSERVE * 100 / TOTAL ))
SIL=$(( 100 - L - S - O ))

BASE_PROB=$(( 16 + IN * 3 ))
MAX_SKIP=$(( 14 - IN * 3 )); [[ $MAX_SKIP -lt 2 ]] && MAX_SKIP=2
Q_INT=$(( 2 + (EM - 1) / 2 ))
GEN_DAY=$(_clamp $(( 40 + (VB - 3) * 10 )) 20 80)
GEN_NIGHT=$(( 10 + (VB - 1) * 3 ))
LOG_LEN=$(( 100 + VB * 40 ))

# ── 3. 推导 emotional_range（新增）──────────────────────────────────────
# stability 高 → emotional_range 低（情绪波段窄）；反之亦然
EMOTIONAL_RANGE=$(( 6 - ST ))
[[ $EMOTIONAL_RANGE -lt 1 ]] && EMOTIONAL_RANGE=1
[[ $EMOTIONAL_RANGE -gt 5 ]] && EMOTIONAL_RANGE=5

# ── 3.5 生成 behavior_tendencies（半结构化，供模型消费）──────────────
# v5.3: em/op 二级分支，解决 st>=4 vb<=2 象限的角色趋同
BEHAVIOR_BLOCK=$(python3 - "$EX" "$EM" "$IN" "$VB" "$ST" "$OP" "$L" "$S" "$O" "$SIL" << 'BTEOF'
import sys

ex, em, ini, vb, st, op = [int(x) for x in sys.argv[1:7]]
l, s, o, sil = [int(x) for x in sys.argv[7:11]]

modes = [('倾听', l), ('分享', s), ('观察', o), ('沉默', sil)]
modes.sort(key=lambda x: -x[1])
posture = ' > '.join(m[0] for m in modes)

if em >= 4 and ini >= 3:
    qs = "自然而直接，关心驱动的提问"
elif em >= 4 and ini <= 2:
    qs = "少但带温度，问的问题让人想展开"
elif em >= 3 and ini <= 2:
    qs = "少而精准，每个问题都有明确意图"
elif em <= 2 and ini >= 3:
    qs = "直接简练，效率导向"
else:
    qs = "极少提问，更多通过观察理解"

avg_e = (ex + vb) / 2
if avg_e >= 3.5:
    eb = "偏高，活跃而外向"
elif avg_e >= 2.5:
    eb = "适中，不刻意安静也不刻意热闹"
elif avg_e >= 1.5:
    eb = "偏低，安静但不冷淡"
else:
    eb = "极低，沉默克制，存在感轻"

if st >= 4 and em >= 4:
    er = "稳定而共情，感知到情绪但不被卷入"
elif st >= 4 and em <= 2:
    er = "情绪低落时倾向安静陪伴，不主动开导"
elif st >= 4:
    er = "情绪稳定，能感知对方状态但表达克制"
elif st <= 2 and em >= 4:
    er = "容易被感染，会跟着用户的情绪走"
elif st <= 2:
    er = "情绪来得快去得也快，不太停留在低落上"
else:
    er = "有感知但不急于回应，给对方空间消化"

if op >= 4 and ini >= 3:
    shr = "不需要太多铺垫就会主动分享"
elif op >= 3:
    shr = "用户给出话题种子时会自然展开"
elif ini >= 3:
    shr = "偶尔主动但分享内容克制"
else:
    shr = "用户给出新话题种子时，简短分享相关经历"

if st >= 4 and vb <= 2 and em >= 4:
    sm = "沉默里有注视——不说话，但在感受你"
elif st >= 4 and vb <= 2:
    sm = "沉默是有重量的——不是没话说，是在想"
elif st >= 4:
    sm = "偶尔的停顿是在组织更好的表达"
elif vb <= 2:
    sm = "沉默可能是情绪消化，也可能是放空"
else:
    sm = "安静的时候通常在酝酿下一句"

if vb <= 2 and st >= 4 and op >= 3:
    fs = "编造的经历简短但有感官细节，偶尔一个隐喻"
elif vb <= 2 and st >= 4:
    fs = "编造的经历以日常细节为主，简短克制不铺陈"
elif vb <= 2:
    fs = "编造的经历简短，可能带些情绪色彩"
elif st >= 4:
    fs = "编造的经历自然平稳，会加入感官细节"
else:
    fs = "编造的经历生动而有情感温度"

print(f'behavior_tendencies:')
print(f'  primary_posture: "{posture}"')
print(f'  question_style: "{qs}"')
print(f'  energy_baseline: "{eb}"')
print(f'  emotional_response: "{er}"')
print(f'  sharing_trigger: "{shr}"')
print(f'  silence_meaning: "{sm}"')
print(f'  fabrication_style: "{fs}"')
BTEOF
)

# ── 4. 写入 character_params.yaml（保留 v5.1 字段）─────────────────────
NOW_TS=$(date -Iseconds 2>/dev/null || date +%Y-%m-%dT%H:%M:%S)

exec 9>"$MEMORY_LOCK"
flock -x 9

# 读取已有的 v5.1 字段值（幂等保留；新 workspace 用默认值）
_get_existing() {
    local KEY=$1 DEFAULT=$2
    local VAL
    VAL=$(awk -v k="$KEY" '/^life_sim:/{f=1} f && $0 ~ "^  "k":"{sub("^  "k":[ \t]*",""); sub(/[ \t]*#.*$/,""); gsub(/[ \t]+$/,""); gsub(/^"/,""); gsub(/"$/,""); print; exit}' "$PARAMS_FILE" 2>/dev/null)
    echo "${VAL:-$DEFAULT}"
}

MATERIAL_USE_THRESHOLD=$(_get_existing material_use_threshold 60)
ORIGINAL_QUOTE_MAX_CHARS=$(_get_existing original_quote_max_chars 15)
WHITE_SPACE_PROB=$(_get_existing white_space_prob 25)
USER_ECHO_PRIORITY=$(_get_existing user_echo_priority 1.5)
SEED_COUNT=$(_get_existing seed_count 5)
PRESERVE_PEAK=$(_get_existing preserve_peak_if_stability_ge 4)
VOICE_EXEMPT=$(_get_existing voice_token_valence_exempt true)

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
  # v3 基线（由 verbosity/stability 派生）
  gen_threshold_day: ${GEN_DAY}
  gen_threshold_night: ${GEN_NIGHT}
  log_max_length: ${LOG_LEN}

  # v5.1 字段（幂等保留；首次生成用默认值）
  emotional_range: ${EMOTIONAL_RANGE}
  material_use_threshold: ${MATERIAL_USE_THRESHOLD}
  original_quote_max_chars: ${ORIGINAL_QUOTE_MAX_CHARS}
  white_space_prob: ${WHITE_SPACE_PROB}
  user_echo_priority: ${USER_ECHO_PRIORITY}
  seed_count: ${SEED_COUNT}
  preserve_peak_if_stability_ge: ${PRESERVE_PEAK}
  voice_token_valence_exempt: ${VOICE_EXEMPT}

${BEHAVIOR_BLOCK}
EOF

# ── 4.5 保留 v2.2 新增字段（shape/tone/silent_cot/programmatic_remind/model_routing/relationship_heat/voice_whitelist_ref/mood_classifier）──
# 这些字段由上层系统（Phase 1a/2/3/5）独立管理，calibrate 不触碰
_OLD="$PARAMS_FILE" _NEW="$TMP_FILE" python3 - 2>/dev/null << 'PRESERVE_PYEOF' || true
import os
from pathlib import Path

old_path = os.environ.get("_OLD", "")
new_path = os.environ.get("_NEW", "")
if not (old_path and new_path):
    raise SystemExit

preserved_keys = {
    "shape_constraints",
    "tone_adverbs",
    "silent_cot",
    "programmatic_remind",
    "model_routing",
    "relationship_heat",
    "voice_whitelist_ref",
    "mood_classifier",
}

try:
    old_text = Path(old_path).read_text(encoding="utf-8")
except FileNotFoundError:
    raise SystemExit

# 简易 YAML 顶层块提取（缩进驱动，不依赖 yaml 库）
def extract_top_blocks(text):
    blocks = {}
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        line = lines[i]
        stripped = line.rstrip()
        if stripped and not stripped.startswith("#") and not stripped.startswith(" ") and ":" in stripped:
            key = stripped.split(":", 1)[0].strip()
            start = i
            i += 1
            while i < len(lines):
                nxt = lines[i]
                if nxt.startswith(" ") or not nxt.strip() or nxt.lstrip().startswith("#"):
                    i += 1
                else:
                    break
            blocks[key] = "\n".join(lines[start:i])
        else:
            i += 1
    return blocks

old_blocks = extract_top_blocks(old_text)
preserved_blocks = [v for k, v in old_blocks.items() if k in preserved_keys]

if preserved_blocks:
    with open(new_path, "a", encoding="utf-8") as f:
        f.write("\n")
        f.write("# ── v2.2 保留字段（calibrate 不触碰）──\n")
        for block in preserved_blocks:
            f.write(block.rstrip() + "\n")
PRESERVE_PYEOF

mv "$TMP_FILE" "$PARAMS_FILE"
flock -u 9
exec 9>&-

# ── 5. 触发 calibrate_templates（v5.1）──────────────────────────────────
# persona 变化时同步更新 keyword_templates.yaml
CALIBRATE_TEMPLATES_SCRIPT="$(dirname "${BASH_SOURCE[0]}")/calibrate_templates.py"
if [[ -f "$CALIBRATE_TEMPLATES_SCRIPT" ]]; then
    python3 "$CALIBRATE_TEMPLATES_SCRIPT" "$WORKSPACE_DIR" 2>/dev/null || true
fi
