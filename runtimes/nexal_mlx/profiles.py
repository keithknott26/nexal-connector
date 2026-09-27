"""Reviewed built-in architectures and fixed chat formats; no model Jinja runs."""

import math
from .security import Rejected, integer

TOKENIZERS = {
    "llama": {"PreTrainedTokenizerFast", "LlamaTokenizer", "LlamaTokenizerFast"},
    "qwen3": {"PreTrainedTokenizerFast", "Qwen2Tokenizer", "Qwen2TokenizerFast"},
    "phi3": {"PreTrainedTokenizerFast", "GPT2Tokenizer", "GPT2TokenizerFast"},
}
CHAT_TOKENS = {
    "qwen3": ("<|im_start|>", "<|im_end|>"),
    "phi3": ("<|system|>", "<|user|>", "<|assistant|>", "<|end|>", "<|endoftext|>"),
}
STOP_TOKENS = {"qwen3": ("<|im_end|>",), "phi3": ("<|end|>", "<|endoftext|>")}


def validate_config(model_type, config):
    if config.get("model_type") != model_type:
        raise Rejected("configuration architecture differs from approved manifest")
    if model_type == "llama":
        return  # Preserve the legacy raw-text profile.
    if config.get("hidden_act", "silu") != "silu" or any(
        config.get(field, False) is not False
        for field in ("attention_bias", "mlp_bias", "lm_head_bias", "use_sliding_window")
    ):
        raise Rejected("unsupported activation, bias, or sliding attention")
    expected = "Qwen3ForCausalLM" if model_type == "qwen3" else "Phi3ForCausalLM"
    if "architectures" in config and config["architectures"] != [expected]:
        raise Rejected("unsupported model class")
    for field in ("hidden_size", "num_hidden_layers", "intermediate_size",
                  "num_attention_heads", "num_key_value_heads", "vocab_size"):
        integer(config.get(field), 1, 1_000_000)
    heads, kv = config["num_attention_heads"], config["num_key_value_heads"]
    if heads % kv:
        raise Rejected("invalid grouped-query attention dimensions")
    if type(config.get("tie_word_embeddings")) is not bool:
        raise Rejected("explicit embedding layout required")
    integer(config.get("max_position_embeddings"), 4608, 1_000_000)
    for field in ("rms_norm_eps", "rope_theta"):
        v = config.get(field)
        if type(v) not in (float, int) or not math.isfinite(v) or v <= 0:
            raise Rejected("invalid normalization or rotary configuration")
    if model_type == "qwen3":
        integer(config.get("head_dim"), 2, 1024)
        if config["head_dim"] % 2:
            raise Rejected("invalid Qwen rotary dimensions")
        if config.get("rope_scaling") is not None:
            raise Rejected("Qwen3 extended rotary scaling is not validated")
    else:
        if config["hidden_size"] % heads:
            raise Rejected("invalid Phi attention dimensions")
        factor = config.get("partial_rotary_factor", 1.0)
        if type(factor) not in (int, float) or not 0 < factor <= 1:
            raise Rejected("invalid partial rotary factor")
        dims = config["hidden_size"] // heads * factor
        if dims != int(dims) or int(dims) < 2 or int(dims) % 2:
            raise Rejected("invalid partial rotary dimensions")
        rope = config.get("rope_scaling")
        if rope is not None:
            if not isinstance(rope, dict) or set(rope) != {"type", "short_factor", "long_factor"} or rope["type"] != "longrope":
                raise Rejected("only reviewed Phi longrope scaling is supported")
            integer(config.get("original_max_position_embeddings"), 1, config["max_position_embeddings"])
            for key in ("short_factor", "long_factor"):
                values = rope[key]
                if not isinstance(values, list) or len(values) != int(dims) // 2 or any(
                    type(v) not in (float, int) or not math.isfinite(v) or v <= 0 for v in values
                ):
                    raise Rejected("invalid Phi longrope factors")


def format_prompt(model_type, prompt):
    if model_type not in TOKENIZERS:
        raise Rejected("unsupported chat profile")
    if model_type == "llama":
        return prompt
    # Prevent evidence from injecting chat role delimiters. This is not a general
    # semantic prompt-injection defense; all output still has advisory authority.
    if "<|" in prompt or "<think>" in prompt or "</think>" in prompt:
        raise Rejected("prompt contains reserved chat delimiters")
    if model_type == "qwen3":
        # Official non-thinking suffix, implemented as fixed code, never Jinja.
        return "<|im_start|>user\n" + prompt + "<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
    return "<|user|>" + prompt + "<|end|><|assistant|>"


def encode_prompt(model_type, tokenizer, prompt):
    formatted = format_prompt(model_type, prompt)
    if model_type == "llama":
        return tokenizer.encode(formatted)
    vocab = tokenizer.get_vocab()
    for marker in CHAT_TOKENS[model_type]:
        token_id = vocab.get(marker)
        if type(token_id) is not int or tokenizer.encode(marker, add_special_tokens=False) != [token_id]:
            raise Rejected("tokenizer lacks required atomic chat tokens")
    # MLX-LM 0.28.4 reads config.eos_token_id, which misses Phi's end-of-turn
    # token in the upstream config. Set both reviewed stop tokens explicitly.
    tokenizer.eos_token_ids = {vocab[t] for t in STOP_TOKENS[model_type]}
    return tokenizer.encode(formatted, add_special_tokens=False)
