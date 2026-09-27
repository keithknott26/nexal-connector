"""Explicit native lab test: tiny random weights + local official tokenizers.

Never certifies a production checkpoint, writes a release approval, or downloads.
Run with a reviewed local environment on Apple silicon; see QWEN-PHI.md.
"""
import faulthandler
faulthandler.dump_traceback_later(45, exit=True)

import argparse
import hashlib
import importlib
import json
import os
from pathlib import Path
import platform
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from nexal_mlx.profiles import encode_prompt, format_prompt, validate_config
from nexal_mlx.security import verify_model


def run(tokenizers_root):
    import mlx.core as mx
    import mlx.nn as nn
    import mlx_lm
    from mlx.utils import tree_flatten
    from mlx_lm.models.cache import make_prompt_cache
    from mlx_lm.sample_utils import make_sampler
    from importlib.metadata import version

    if platform.system() != 'Darwin' or platform.machine() != 'arm64' or not mx.metal.is_available():
        raise RuntimeError('Apple-silicon Metal required')
    provenance=json.loads((tokenizers_root/'provenance.json').read_text())
    report={'fixture_only':True,'production_checkpoint_validated':False,
            'python':platform.python_version(),'macos':platform.mac_ver()[0],
            'packages':{p:version(p) for p in ('mlx','mlx-lm','transformers','tokenizers')},'cases':[]}
    for kind,repo in [('qwen3','Qwen/Qwen3-4B'),('phi3','microsoft/Phi-4-mini-instruct')]:
        source=tokenizers_root/repo.split('/')[-1]
        prov=provenance[repo]
        for name,pin in prov['sha256'].items():
            assert hashlib.sha256((source/name).read_bytes()).hexdigest()==pin
        official=json.loads((source/'config.json').read_text())
        validate_config(kind, official)
        # Upstream Phi contains auto_map and _name_or_path. Deliberately do not
        # copy them into our locally authored tiny test model configuration.
        config={k:official[k] for k in ('model_type','vocab_size','max_position_embeddings',
                                       'rope_theta','rms_norm_eps','tie_word_embeddings','eos_token_id','transformers_version')}
        config.update(hidden_size=64,num_hidden_layers=2,intermediate_size=128,
                      num_attention_heads=4,num_key_value_heads=2)
        if kind=='qwen3':config['head_dim']=16
        else:config.update(partial_rotary_factor=0.75,original_max_position_embeddings=4096,
                           rope_scaling={'type':'longrope','short_factor':[1.0]*6,'long_factor':[2.0]*6})
        validate_config(kind, config)
        module=importlib.import_module('mlx_lm.models.'+kind)
        for bits in (None,4):
            mx.random.seed(11)
            model=module.Model(module.ModelArgs.from_dict(config))
            if bits:
                nn.quantize(model,group_size=32,bits=bits)
                config['quantization']={'group_size':32,'bits':bits}
            model.eval()
            mx.eval(model.parameters())
            with tempfile.TemporaryDirectory() as td:
                root=Path(td).resolve()
                weights=dict(tree_flatten(model.parameters()))
                mx.save_safetensors(str(root/'model.safetensors'),weights)
                (root/'config.json').write_text(json.dumps(config))
                for name in ('tokenizer.json','tokenizer_config.json'):
                    (root/name).write_bytes((source/name).read_bytes())
                files={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in root.iterdir()}
                size=(root/'model.safetensors').stat().st_size
                manifest={'schema_version':1,'model_id':'tiny-random-'+kind,'revision':prov['revision'],
                          'license':'synthetic-weights-official-tokenizer-test-only','model_type':kind,'files':files,
                          'memory':dict(weights_bytes=size,kv_bytes_per_token=1024,activation_bytes=1<<20,
                                        buffer_bytes=1<<20,load_peak_bytes=size*2,safety_bytes=1<<20)}
                raw=json.dumps(manifest).encode();(root/'nexal-model-manifest.json').write_bytes(raw)
                verify_model(str(root),hashlib.sha256(raw).hexdigest())
                loaded,tok=mlx_lm.load(str(root),tokenizer_config={'trust_remote_code':False,'local_files_only':True,'use_fast':True})
                prompt='Summarize this harmless test event.'
                # Golden comparison with the OFFICIAL template in this lab only.
                # Runtime formatting never evaluates the downloaded Jinja.
                expected=tok.apply_chat_template([{'role':'user','content':prompt}],tokenize=False,
                                                add_generation_prompt=True,**({'enable_thinking':False} if kind=='qwen3' else {}))
                assert expected==format_prompt(kind,prompt),(kind,'template mismatch')
                tokens=encode_prompt(kind,tok,prompt)
                from tokenizers import Tokenizer
                raw_tokenizer=Tokenizer.from_file(str(source/'tokenizer.json'))
                assert tokens==raw_tokenizer.encode(expected,add_special_tokens=False).ids,(kind,'official tokenization mismatch')
                x=mx.array([tokens])
                direct=loaded(x);original=model(x)
                delta=float(mx.max(mx.abs(direct-original)).item())
                assert delta<1e-5,(kind,'reload differs',delta)
                cache=make_prompt_cache(loaded)
                loaded(x[:,:-1],cache=cache)
                cached=loaded(x[:,-1:],cache=cache)
                cache_delta=float(mx.max(mx.abs(direct[:,-1:]-cached)).item())
                assert cache_delta<0.01,(kind,'cache differs',cache_delta)
                outputs=[]
                for _ in range(2):
                    outputs.append([r.token for r in mlx_lm.stream_generate(loaded,tok,prompt=tokens,max_tokens=4,sampler=make_sampler(temp=0))])
                assert outputs[0]==outputs[1],(kind,'greedy mismatch')
                report['cases'].append({'model_type':kind,'bits':bits,'tokens':len(tokens),
                                        'reload_max_error':delta,'cache_max_error':cache_delta,
                                        'greedy_repeatable':True,'official_template_matches':True,'official_token_ids_match':True,
                                        'generated_tokens':len(outputs[0]),'peak_memory_bytes':mx.get_peak_memory()})
            del model,loaded,weights,direct,original,cached,cache
            mx.clear_cache()
    return report


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--tokenizers-root',type=Path,required=True)
    args=parser.parse_args()
    os.environ.update(HF_HUB_OFFLINE='1',TRANSFORMERS_OFFLINE='1',HF_HUB_DISABLE_TELEMETRY='1')
    print(json.dumps(run(args.tokenizers_root.resolve()),indent=2))
