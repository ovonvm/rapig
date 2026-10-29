using System.Diagnostics;
using System.Text;

namespace Rapig.Gui.Services;

public sealed record CliResult(bool Ok, string Output);

public static class CliRunner
{
    public static async Task<CliResult> RunAsync(string exe, string arguments, string? workingDirectory = null, string? stdin = null, int timeoutSeconds = 90)
    {
        var psi = new ProcessStartInfo
        {
            FileName = exe,
            Arguments = arguments,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            RedirectStandardInput = stdin != null,
            WorkingDirectory = workingDirectory ?? AppContext.BaseDirectory,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
        };
        try
        {
            using var proc = Process.Start(psi);
            if (proc == null)
            {
                return new CliResult(false, "无法启动进程");
            }
            var sb = new StringBuilder();
            proc.OutputDataReceived += (_, e) => { if (e.Data != null) { sb.AppendLine(e.Data); } };
            proc.ErrorDataReceived += (_, e) => { if (e.Data != null) { sb.AppendLine(e.Data); } };
            proc.BeginOutputReadLine();
            proc.BeginErrorReadLine();
            if (stdin != null)
            {
                await proc.StandardInput.WriteLineAsync(stdin);
                await proc.StandardInput.FlushAsync();
                proc.StandardInput.Close();
            }
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(timeoutSeconds));
            try
            {
                await proc.WaitForExitAsync(cts.Token);
            }
            catch (OperationCanceledException)
            {
                try { proc.Kill(true); } catch { }
                return new CliResult(false, sb.ToString() + "\n[超时]");
            }
            return new CliResult(proc.ExitCode == 0, sb.ToString());
        }
        catch (Exception ex)
        {
            return new CliResult(false, ex.Message);
        }
    }
}
