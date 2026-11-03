#property strict
#property version   "1.001"
#property description "Reports isolated MT5 terminal login and trade readiness to the Go managed worker."

input string ProbeStatusFileName = "algentra-login-probe.json";
bool g_status_write_error_logged = false;
string g_last_status_log = "";
string g_last_gold_symbol_log = "";
datetime g_gold_chart_last_log = 0;
datetime g_gold_chart_last_check = 0;
ulong g_last_single_chart_check_ms = 0;
string g_probe_gold_symbol = "";

string EscapeJSON(string value)
  {
   StringReplace(value, "\\", "\\\\");
   StringReplace(value, "\"", "\\\"");
   StringReplace(value, "\r", " ");
   StringReplace(value, "\n", " ");
   return value;
  }

void WriteStatusToFile(const string file_name,
                       const bool ready,
                       const bool valid,
                       const string message,
                       const bool common)
  {
   int flags = FILE_WRITE|FILE_TXT|FILE_ANSI|FILE_SHARE_READ|FILE_SHARE_WRITE;
   if(common)
      flags |= FILE_COMMON;
   int file = FileOpen(file_name, flags);
   if(file == INVALID_HANDLE)
     {
      if(!g_status_write_error_logged)
        {
         Print("[MT5LoginProbe] cannot write ", common ? "common" : "portable",
               " status file ", file_name, "; error ", GetLastError());
         g_status_write_error_logged = true;
        }
      return;
     }
   g_status_write_error_logged = false;
   string safe_message = EscapeJSON(message);
   string server = EscapeJSON(AccountInfoString(ACCOUNT_SERVER));
   string selected_symbol = EscapeJSON(g_probe_gold_symbol);
   FileWriteString(file, StringFormat("{\"ready\":%s,\"valid\":%s,\"message\":\"%s\",\"login\":%I64d,\"server\":\"%s\",\"symbol\":\"%s\"}",
                                      ready ? "true" : "false",
                                      valid ? "true" : "false",
                                      safe_message,
                                      AccountInfoInteger(ACCOUNT_LOGIN),
                                      server,
                                      selected_symbol));
   FileFlush(file);
   FileClose(file);
  }

void WriteStatus(const bool ready, const bool valid, const string message)
  {
   string safe_message = EscapeJSON(message);
   string server = EscapeJSON(AccountInfoString(ACCOUNT_SERVER));
   string selected_symbol = EscapeJSON(g_probe_gold_symbol);
   string payload = StringFormat("{\"ready\":%s,\"valid\":%s,\"message\":\"%s\",\"login\":%I64d,\"server\":\"%s\",\"symbol\":\"%s\"}",
                                  ready ? "true" : "false",
                                  valid ? "true" : "false",
                                  safe_message,
                                  AccountInfoInteger(ACCOUNT_LOGIN),
                                  server,
                                  selected_symbol);
   // Give the worker a log-based fallback if terminal file access is blocked.
   if(payload != g_last_status_log)
     {
      Print("[MT5LoginProbe] STATUS ", payload);
      g_last_status_log = payload;
     }
   // The worker runs this terminal in portable mode, so its local MQL5/Files
   // directory is deterministic. Also write to FILE_COMMON for diagnostics and
   // compatibility with non-portable terminal layouts.
   WriteStatusToFile(ProbeStatusFileName, ready, valid, message, false);
   WriteStatusToFile(ProbeStatusFileName, ready, valid, message, true);
  }

bool ResolveGoldSymbol(string &resolved)
  {
   resolved = "";
   string best = "";
   string best_upper = "";
   long best_quote_time = 0;
   int best_history = 0; // 1 when the broker actually serves M1 bars for this symbol
   int best_exact_rank = 0;
   int best_suffix_length = 2147483647;
   int best_data_rank = -1; // fresh quote > local/server M1 history > catalog only
   int symbols_total = SymbolsTotal(false);
   for(int i = 0; i < symbols_total; i++)
     {
      string candidate = SymbolName(i, false);
      string upper = candidate;
      StringToUpper(upper);
      if(SymbolInfoInteger(candidate, SYMBOL_TRADE_MODE) == SYMBOL_TRADE_MODE_DISABLED ||
         !SymbolSelect(candidate, true))
         continue;
      string base_currency = SymbolInfoString(candidate, SYMBOL_CURRENCY_BASE);
      string profit_currency = SymbolInfoString(candidate, SYMBOL_CURRENCY_PROFIT);
      StringToUpper(base_currency);
      StringToUpper(profit_currency);
      bool gold_candidate = (StringLen(candidate) >= 6 && StringSubstr(upper, 0, 6) == "XAUUSD") ||
                           (base_currency == "XAU" && profit_currency == "USD");
      if(!gold_candidate)
         continue;
      MqlTick tick;
      long quote_time = 0;
      if(SymbolInfoTick(candidate, tick) && tick.bid > 0.0 && tick.ask > 0.0)
         quote_time = tick.time_msc > 0 ? tick.time_msc : (long)tick.time * 1000;
      long server_now = (long)TimeTradeServer() * 1000;
      // Ignore stale ticks so a weekend/closed market falls back to history
      // and then a broker suffix instead of preferring an inactive bare symbol.
      if(server_now > 0 && quote_time < server_now - 30 * 60 * 1000)
         quote_time = 0;
      bool has_xauusd_prefix = StringSubstr(upper, 0, 6) == "XAUUSD";
      int suffix_length = has_xauusd_prefix ? StringLen(candidate) - 6 : StringLen(candidate);
      // When no quote/history is available yet, prefer the broker's suffixed
      // instrument over a bare catalog placeholder such as XAUUSD.
      int exact_rank = suffix_length == 0 ? 0 : 1;
      // A bare catalog entry can exist without account data. A fresh quote is
      // strongest evidence of the active account instrument; history is next.
      int history = SeriesInfoInteger(candidate, PERIOD_M1, SERIES_BARS_COUNT) > 0 ? 1 : 0;
      int data_rank = quote_time > 0 ? 2 : (history > 0 ? 1 : 0);
      bool better = best == "" || data_rank > best_data_rank;
      if(!better && data_rank == best_data_rank)
        {
         if(data_rank == 2 && quote_time != best_quote_time)
            better = quote_time > best_quote_time;
         else if(history != best_history)
            better = history > best_history;
         else if(exact_rank != best_exact_rank)
            better = exact_rank > best_exact_rank;
         else if(suffix_length != best_suffix_length)
            better = suffix_length < best_suffix_length;
         else
            better = upper < best_upper;
        }
      if(better)
        {
         best = candidate;
         best_upper = upper;
         best_data_rank = data_rank;
         best_history = history;
         best_quote_time = quote_time;
         best_exact_rank = exact_rank;
         best_suffix_length = suffix_length;
        }
     }
   if(best == "" || !SymbolSelect(best, true))
      return false;
   resolved = best;
   return true;
  }

void KeepSingleProbeChart()
  {
   ulong now_ms = GetTickCount64();
   if(g_last_single_chart_check_ms > 0 && now_ms - g_last_single_chart_check_ms < 5000)
      return;
   g_last_single_chart_check_ms = now_ms;
   long active_chart = ChartID();
   long chart_id = ChartFirst();
   while(chart_id >= 0)
     {
      long next_chart = ChartNext(chart_id);
      if(chart_id != active_chart)
         ChartClose(chart_id);
      chart_id = next_chart;
     }
   ChartSetInteger(active_chart, CHART_BRING_TO_TOP, true);
   ChartRedraw(active_chart);
  }

void EnsureDefaultGoldChart()
  {
   if(g_gold_chart_last_check > 0 && TimeCurrent() - g_gold_chart_last_check < 10)
      return;
   g_gold_chart_last_check = TimeCurrent();

   string gold_symbol;
   if(!ResolveGoldSymbol(gold_symbol))
     {
      if(TimeCurrent() - g_gold_chart_last_log >= 15)
        {
         Print("[MT5LoginProbe] XAUUSD belum ditemukan di daftar simbol broker.");
         g_gold_chart_last_log = TimeCurrent();
        }
      return;
     }
   if(g_last_gold_symbol_log != gold_symbol)
     {
      Print("[MT5LoginProbe] simbol gold dari katalog akun: ", gold_symbol);
      g_last_gold_symbol_log = gold_symbol;
     }
   if(_Symbol == gold_symbol && _Period == PERIOD_M1)
     {
      int bars = (int)SeriesInfoInteger(gold_symbol, PERIOD_M1, SERIES_BARS_COUNT);
      if(bars > 0)
         Print("[MT5LoginProbe] chart probe siap: ", gold_symbol, ", M1, bars=", bars);
      else if(TimeCurrent() - g_gold_chart_last_log >= 15)
        {
         Print("[MT5LoginProbe] menunggu riwayat M1 untuk simbol gold akun: ", gold_symbol);
         g_gold_chart_last_log = TimeCurrent();
        }
      return;
     }
   ResetLastError();
   if(ChartSetSymbolPeriod(0, gold_symbol, PERIOD_M1))
     {
      Print("[MT5LoginProbe] chart probe memakai ", gold_symbol, ", M1.");
     }
   else if(TimeCurrent() - g_gold_chart_last_log >= 15)
     {
      Print("[MT5LoginProbe] gagal mengganti chart ke ", gold_symbol,
            "; error ", GetLastError());
      g_gold_chart_last_log = TimeCurrent();
     }
  }

int OnInit()
  {
   // This terminal is short-lived and only validates broker authentication.
   // Report immediately; chart discovery/history requests belong to the
   // persistent copier EA and must not delay the portal's login result.
   WriteStatus(false, false, "Menunggu koneksi broker MT5.");
   KeepSingleProbeChart();
   if(!EventSetTimer(1))
     {
      Print("[MT5LoginProbe] gagal mengaktifkan timer; error ", GetLastError());
      return INIT_FAILED;
     }
   return INIT_SUCCEEDED;
  }

void OnDeinit(const int reason)
  {
   EventKillTimer();
  }

void OnTimer()
  {
   // One terminal, one chart: close charts restored from the template profile.
   KeepSingleProbeChart();
   if(!TerminalInfoInteger(TERMINAL_CONNECTED) || AccountInfoInteger(ACCOUNT_LOGIN) <= 0)
     {
      WriteStatus(false, false, "Menunggu koneksi broker MT5.");
      return;
     }
   if(!AccountInfoInteger(ACCOUNT_TRADE_ALLOWED))
     {
      WriteStatus(true, false, "Login broker berhasil, tetapi broker menolak izin trading akun ini.");
      return;
     }
   if(!TerminalInfoInteger(TERMINAL_TRADE_ALLOWED))
     {
      WriteStatus(true, false, "Login berhasil, tetapi izin Auto Trading terminal belum aktif.");
      return;
     }
   if(!MQLInfoInteger(MQL_TRADE_ALLOWED))
     {
      WriteStatus(true, false, "Login berhasil, tetapi Expert tidak diizinkan trading (Allow Algo Trading pada Expert).");
      return;
     }
   if(g_probe_gold_symbol == "")
     {
      WriteStatus(false, false, "Login broker berhasil; sedang memilih simbol gold yang didukung akun.");
      if(!ResolveGoldSymbol(g_probe_gold_symbol))
        {
         WriteStatus(true, false, "Login berhasil, tetapi simbol gold XAUUSD tidak ditemukan pada daftar simbol akun.");
         return;
        }
      Print("[MT5LoginProbe] simbol gold akun terpilih saat login: ", g_probe_gold_symbol);
     }
   if(_Symbol != g_probe_gold_symbol || _Period != PERIOD_M1)
     {
      ResetLastError();
      if(!ChartSetSymbolPeriod(0, g_probe_gold_symbol, PERIOD_M1))
        {
         WriteStatus(true, false, "Login berhasil, tetapi chart tidak dapat dialihkan ke simbol akun " + g_probe_gold_symbol + ".");
         return;
        }
      WriteStatus(false, false, "Login broker berhasil; memilih " + g_probe_gold_symbol + " pada timeframe M1.");
      return;
     }
   WriteStatus(true, true, "Login broker berhasil; simbol akun " + g_probe_gold_symbol + " dipilih pada M1.");
  }
