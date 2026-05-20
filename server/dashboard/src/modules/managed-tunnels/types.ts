export type TunnelState = 'disabled' | 'connecting' | 'live' | 'failed';

export type TunnelStatus = {
  provider: string;
  state: TunnelState;
  mode?: string;
  public_url?: string;
  pid?: number;
  uptime_sec?: number;
  last_error?: string;
  available: boolean;
  note?: string;
};

export type TunnelCatalog = {
  providers: TunnelStatus[];
};

export type TunnelTestResult = {
  ok: boolean;
  public_url?: string;
  status_code?: number;
  latency_ms?: number;
  detail?: string;
};

export type TunnelMode = 'named' | 'quick';

export type TunnelConfig = {
  enabled: boolean;
  provider: string;
  mode: TunnelMode;
  hostname: string;
  token_set: boolean;
  updated_at?: string;
  updated_by?: string;
};

export type TunnelProvider = 'cloudflare' | 'ngrok';

export type TunnelBinary = {
  provider: TunnelProvider;
  installed: boolean;
  managed: boolean;
  path?: string;
  version?: string;
};

export type TunnelBinaryList = {
  binaries: TunnelBinary[];
};

export type TunnelConfigUpdate = {
  enabled?: boolean;
  provider?: TunnelProvider;
  mode?: TunnelMode;
  hostname?: string;
  token?: string;
};
