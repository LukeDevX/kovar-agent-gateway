#!/usr/bin/env python3
"""Regenerate the embedded request schemas from the authoritative local JSON."""
import json
from pathlib import Path
source = json.loads(Path('kovar-new-api.json').read_text())
operations = {
    'chat': '/v1/chat/completions',
    'image': '/v1/images/generations/',
    'image_edit': '/v1/images/edits/',
    'qwen_image': '/v1/images/generations',
    'qwen_image_edit': '/v1/images/edits',
    'video': '/v1/video/generations',
    'speech': '/v1/audio/speech',
    'transcription': '/v1/audio/transcriptions',
    'embedding': '/v1/embeddings',
    'rerank': '/v1/rerank',
}
def clean(value):
    if isinstance(value, dict):
        result = {k: clean(v) for k, v in value.items()
                  if not k.startswith('x-') and k not in ('description', 'examples', 'example')}
        # The export assigns type:string AND oneOf:string|array to chat content.
        # Preserve the explicitly described union instead of rejecting arrays.
        if 'oneOf' in result:
            result.pop('type', None)
        return result
    if isinstance(value, list):
        return [clean(v) for v in value]
    return value
for name, path in operations.items():
    content = source['paths'][path]['post']['requestBody']['content']
    schema = clean(next(iter(content.values()))['schema'])
    # Gateway only accepts upstream fields documented at the request root.
    schema['additionalProperties'] = False
    Path(f'internal/client/kovarmodel/schemas/{name}.json').write_text(
        json.dumps(schema, ensure_ascii=False, indent=2) + '\n')
