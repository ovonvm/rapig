using System.Diagnostics;
using System.IO;
using System.IO.Compression;
using System.Net.Http;
using System.Text;

namespace Rapig.Gui.Services;

public enum CoreHealth
{
    Stopped,
    Unhealthy,
    Running,
}

/// <summary>
/// 管理 rapig 核进程（壳+核架构：GUI 只负责启停与调用核的 CLI/HTTP API）。
/// 基础模式下不创建任何目录或文件：数据目录由核自己创建，日志只保留在内存里。
/// </summary>
public sealed class CoreController
{
    private const int MaxLogLines = 100;

    private readonly HttpClient http = new() { Timeout = TimeSpan.FromSeconds(2) };
    private readonly List<string> logLines = new();
    private readonly object logLock = new();

    public GuiConfig Config { get; }

    public static string Root => AppContext.BaseDirectory;

    public string DataDir => Config.DataDir;

    public string CoreExe => string.IsNullOrWhiteSpace(Config.CorePath)
        ? GuiConfig.DefaultCorePath
        : (Path.IsPathRooted(Config.CorePath) ? Config.CorePath : Path.Combine(Root, Config.CorePath));

    /// <summary>
    /// 把本 exe 内嵌的核心程序释放到 %ProgramData%\Rapig\core\rapig-core.exe。
    /// 单文件发布时核心作为 EmbeddedResource 打包，GUI 运行时才有磁盘文件可启动。
    /// </summary>
    public bool EnsureCore()
    {
        try
        {
            var target = CoreExe;
            var dir = Path.GetDirectoryName(target)!;
            Directory.CreateDirectory(dir);

            using var stream = typeof(CoreController).Assembly
                .GetManifestResourceStream("Rapig.Gui.payload.rapig-core.exe");
            if (stream == null)
            {
                // 开发态（未打包 payload）回退到仓库构建产物。
                foreach (var candidate in new[]
                         {
                             Path.Combine(Root, "rapig-core.exe"),
                             Path.Combine(Root, "..", "..", "..", "..", "dist", "rapig.exe"),
                         })
                {
                    if (File.Exists(candidate))
                    {
                        File.Copy(candidate, target, overwrite: true);
                        return true;
                    }
                }
                return false;
            }

            var tmp = target + ".tmp";
            using (var file = File.Create(tmp))
            {
                stream.CopyTo(file);
            }
            if (File.Exists(target))
            {
                File.Replace(tmp, target, null);
            }
            else
            {
                File.Move(tmp, target);
            }
            return true;
        }
        catch
        {
            return false;
        }
    }

    /// <summary>首次运行且库中没有账号时创建管理员，密码由调用方生成并展示一次。</summary>
    public async Task<(bool Ok, string Message)> InitAdminAsync(string password)
    {
        if (!File.Exists(CoreExe))
        {
            return (false, "核心程序不存在");
        }
        var tmp = Path.Combine(Path.GetTempPath(), "rapig-init-" + Guid.NewGuid().ToString("N") + ".txt");
        try
        {
            await File.WriteAllTextAsync(tmp, password);
            var result = await CliRunner.RunAsync(
                CoreExe,
                $"init-admin -data \"{DataDir}\" -password-file \"{tmp}\"",
                Root);
            return (result.Ok, result.Output.Trim());
        }
        finally
        {
            try { File.Delete(tmp); } catch { /* 忽略。 */ }
        }
    }

    public string PidFile => Path.Combine(DataDir, "rapig.pid");

    public string HealthUrl => $"http://127.0.0.1:{Config.Port}/health";

    public string AppUrl => $"http://127.0.0.1:{Config.Port}/";

    public CoreController(GuiConfig config) => Config = config;

    public async Task<bool> HealthAsync()
    {
        try
        {
            using var resp = await http.GetAsync(HealthUrl);
            return resp.IsSuccessStatusCode;
        }
        catch
        {
            return false;
        }
    }

    public int ReadPid()
    {
        try
        {
            if (File.Exists(PidFile) && int.TryParse(File.ReadAllText(PidFile).Trim(), out var pid))
            {
                return pid;
            }
        }
        catch
        {
            // 忽略。
        }
        return 0;
    }

    public static bool PidAlive(int pid)
    {
        if (pid <= 0)
        {
            return false;
        }
        try
        {
            using var p = Process.GetProcessById(pid);
            return !p.HasExited;
        }
        catch
        {
            return false;
        }
    }

    public async Task<CoreHealth> ProbeAsync()
    {
        if (await HealthAsync())
        {
            return CoreHealth.Running;
        }
        return PidAlive(ReadPid()) ? CoreHealth.Unhealthy : CoreHealth.Stopped;
    }

    private string CoreArguments()
    {
        var sb = new StringBuilder();
        sb.Append("serve -addr 127.0.0.1:").Append(Config.Port);
        sb.Append(" -data \"").Append(DataDir).Append('"');
        sb.Append(" -profile ").Append(Config.Profile);
        sb.Append(" -log-level ").Append(Config.LogLevel);
        if (Config.CookieSecure)
        {
            sb.Append(" -cookie-secure");
        }
        return sb.ToString();
    }

    public async Task<(bool Ok, string Message)> StartAsync()
    {
        if (await HealthAsync())
        {
            return (true, "网盘已在运行");
        }
        if (!File.Exists(CoreExe))
        {
            return (false, $"找不到核心程序：{CoreExe}");
        }

        var psi = new ProcessStartInfo
        {
            FileName = CoreExe,
            Arguments = CoreArguments(),
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            WorkingDirectory = Root,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
        };
        try
        {
            var proc = Process.Start(psi);
            if (proc == null)
            {
                return (false, "无法启动网盘进程");
            }
            proc.OutputDataReceived += (_, e) => AppendLog(e.Data);
            proc.ErrorDataReceived += (_, e) => AppendLog(e.Data);
            proc.BeginOutputReadLine();
            proc.BeginErrorReadLine();
        }
        catch (Exception ex)
        {
            return (false, "启动失败：" + ex.Message);
        }

        return await WaitHealthyAsync(20) ? (true, "已启动") : (false, "启动超时（20 秒），请查看下方日志");
    }

    public async Task<bool> WaitHealthyAsync(int timeoutSeconds)
    {
        var deadline = DateTime.UtcNow.AddSeconds(timeoutSeconds);
        while (DateTime.UtcNow < deadline)
        {
            if (await HealthAsync())
            {
                return true;
            }
            await Task.Delay(500);
        }
        return false;
    }

    public async Task<(bool Ok, string Message)> StopAsync()
    {
        var pid = ReadPid();
        if (pid <= 0 || !PidAlive(pid))
        {
            TryDeletePid();
            return (true, "网盘未在运行");
        }
        await Task.Run(() =>
        {
            try
            {
                using var k = Process.Start(new ProcessStartInfo("taskkill", $"/PID {pid}")
                {
                    UseShellExecute = false,
                    CreateNoWindow = true,
                });
                k?.WaitForExit(5000);
            }
            catch
            {
                // 忽略。
            }
        });
        for (var i = 0; i < 12; i++)
        {
            if (!PidAlive(pid))
            {
                TryDeletePid();
                return (true, "已停止");
            }
            await Task.Delay(500);
        }
        await Task.Run(() =>
        {
            try
            {
                using var k = Process.Start(new ProcessStartInfo("taskkill", $"/PID {pid} /F")
                {
                    UseShellExecute = false,
                    CreateNoWindow = true,
                });
                k?.WaitForExit(5000);
            }
            catch
            {
                // 忽略。
            }
        });
        await Task.Delay(500);
        TryDeletePid();
        return (true, "已强制停止（SQLite WAL 会在下次打开时自动恢复）");
    }

    public async Task<(bool Ok, string Message)> RestartAsync()
    {
        var (ok, msg) = await StopAsync();
        return !ok ? (false, msg) : await StartAsync();
    }

    public void OpenBrowser()
    {
        try
        {
            Process.Start(new ProcessStartInfo(AppUrl) { UseShellExecute = true });
        }
        catch
        {
            // 忽略。
        }
    }

    public void OpenDataFolder()
    {
        try
        {
            if (Directory.Exists(DataDir))
            {
                Process.Start(new ProcessStartInfo("explorer.exe", $"\"{DataDir}\"") { UseShellExecute = true });
            }
            else
            {
                Process.Start(new ProcessStartInfo(GuiConfig.MachineRoot) { UseShellExecute = true });
            }
        }
        catch
        {
            // 忽略。
        }
    }

    public async Task<string> RunDoctorAsync()
    {
        var r = await CliRunner.RunAsync(CoreExe, $"doctor -data \"{DataDir}\"", Root);
        return r.Output;
    }

    public async Task<(bool Ok, string Message)> ResetPasswordAsync(string password)
    {
        var wasRunning = await HealthAsync();
        await StopAsync();
        var r = await CliRunner.RunAsync(CoreExe, $"reset-admin-password -data \"{DataDir}\" -password-stdin", Root, stdin: password);
        if (!r.Ok)
        {
            return (false, r.Output);
        }
        if (wasRunning)
        {
            var (ok, msg) = await StartAsync();
            if (!ok)
            {
                return (false, "密码已重置，但重启网盘失败：" + msg);
            }
        }
        return (true, r.Output);
    }

    public async Task<(bool Ok, string Message)> BackupAsync()
    {
        try
        {
            var dir = GuiConfig.BackupDir;
            Directory.CreateDirectory(dir);
            var dest = Path.Combine(dir, $"rapig-{DateTime.Now:yyyyMMdd-HHmmss}.zip");
            await Task.Run(() => ZipFile.CreateFromDirectory(DataDir, dest));
            return (true, dest);
        }
        catch (Exception ex)
        {
            return (false, ex.Message);
        }
    }

    public string SnapshotLog()
    {
        lock (logLock)
        {
            return string.Join(Environment.NewLine, logLines);
        }
    }

    private void AppendLog(string? line)
    {
        if (line == null)
        {
            return;
        }
        lock (logLock)
        {
            logLines.Add(line);
            if (logLines.Count > MaxLogLines)
            {
                logLines.RemoveRange(0, logLines.Count - MaxLogLines);
            }
        }
    }

    public void ClearLog()
    {
        lock (logLock)
        {
            logLines.Clear();
        }
    }

    private void TryDeletePid()
    {
        try
        {
            if (File.Exists(PidFile))
            {
                File.Delete(PidFile);
            }
        }
        catch
        {
            // 忽略。
        }
    }

    public void Dispose() => http.Dispose();
}
