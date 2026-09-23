export interface SiteStatus {
  github_oauth?: boolean;
  github_client_id?: string;
  wechat_login?: boolean;
  wechat_qrcode?: string;
  telegram_oauth?: boolean;
  telegram_bot_name?: string;
  turnstile_check?: boolean;
  turnstile_site_key?: string;
  email_verification?: boolean;
  top_up_link?: string;
  min_topup?: number;
  enable_online_topup?: boolean;
  server_address?: string;
}
