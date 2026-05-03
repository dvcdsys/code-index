import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import type {
  DefinitionRequest,
  DefinitionResponse,
  FileSearchRequest,
  FileSearchResponse,
  ReferenceRequest,
  ReferenceResponse,
  SemanticSearchRequest,
  SemanticSearchResponse,
  SymbolSearchRequest,
  SymbolSearchResponse,
} from '@/api/types';

export type SearchMode = 'semantic' | 'symbols' | 'definitions' | 'references' | 'files';

export const SEARCH_MODES: { id: SearchMode; label: string; description: string }[] = [
  { id: 'semantic', label: 'Semantic', description: 'Vector search: ask in natural language.' },
  { id: 'symbols', label: 'Symbols', description: 'Find symbols by name (substring match).' },
  { id: 'definitions', label: 'Definitions', description: 'Where is this symbol defined?' },
  { id: 'references', label: 'References', description: 'Where is this symbol used?' },
  { id: 'files', label: 'Files', description: 'Find files by path substring.' },
];

const baseKey = ['search'] as const;

function searchEnabled(query: string) {
  return query.trim().length >= 2;
}

export function useSemanticSearch(
  projectHash: string | undefined,
  body: SemanticSearchRequest
) {
  return useQuery({
    queryKey: [...baseKey, 'semantic', projectHash, body],
    queryFn: ({ signal }) =>
      api.post<SemanticSearchResponse>(`/projects/${projectHash}/search`, body, { signal }),
    enabled: Boolean(projectHash) && searchEnabled(body.query),
  });
}

export function useSymbolSearch(
  projectHash: string | undefined,
  body: SymbolSearchRequest
) {
  return useQuery({
    queryKey: [...baseKey, 'symbols', projectHash, body],
    queryFn: ({ signal }) =>
      api.post<SymbolSearchResponse>(`/projects/${projectHash}/search/symbols`, body, { signal }),
    enabled: Boolean(projectHash) && searchEnabled(body.query),
  });
}

export function useDefinitions(
  projectHash: string | undefined,
  body: DefinitionRequest
) {
  return useQuery({
    queryKey: [...baseKey, 'definitions', projectHash, body],
    queryFn: ({ signal }) =>
      api.post<DefinitionResponse>(`/projects/${projectHash}/search/definitions`, body, { signal }),
    enabled: Boolean(projectHash) && searchEnabled(body.symbol),
  });
}

export function useReferences(
  projectHash: string | undefined,
  body: ReferenceRequest
) {
  return useQuery({
    queryKey: [...baseKey, 'references', projectHash, body],
    queryFn: ({ signal }) =>
      api.post<ReferenceResponse>(`/projects/${projectHash}/search/references`, body, { signal }),
    enabled: Boolean(projectHash) && searchEnabled(body.symbol),
  });
}

export function useFileSearch(
  projectHash: string | undefined,
  body: FileSearchRequest
) {
  return useQuery({
    queryKey: [...baseKey, 'files', projectHash, body],
    queryFn: ({ signal }) =>
      api.post<FileSearchResponse>(`/projects/${projectHash}/search/files`, body, { signal }),
    enabled: Boolean(projectHash) && searchEnabled(body.query),
  });
}
