using System.IO;
using System.Text.Json;

namespace Rapig.Gui.Services;

/// <summary>
/// GUI 配置。本版本固定以管理员运行，数据与配置都放在机器级目录
/// %ProgramData%\Rapig（无需用户选择，避免目录不可写）。
/// </summary>
public sealed class GuiConfig
{
    public string CorePath { get; set; } = "";
    public int Port { get; set; } = 50011;
    public string DataDir { get; set; } = "";
    public string Profile { get; set; } = "low";
    public string LogLevel { get; set; } = "info";
    public bool CookieSecure { get; set; }
    public bool AutoStart { get; set; }

    private static readonly JsonSerializerOptions JsonOptions = new() { WriteIndented = true };

    /// <summary>%ProgramData%\Rapig，机器级可写目录（需管理员）。</summary>
    public static string MachineRoot => Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "Rapig");

    public static string ConfigPath => Path.Combine(MachineRoot, "gui-config.json");

    public static string DefaultDataDir => Path.Combine(MachineRoot, "data");

    public static string CoreDir => Path.Combine(MachineRoot, "core");

    public static string DefaultCorePath => Path.Combine(CoreDir, "rapig-core.exe");

    public static string BackupDir => Path.Combine(MachineRoot, "backups");

    public static GuiConfig Load()
    {
        try
        {
            if (File.Exists(ConfigPath))
            {
                var cfg = JsonSerializer.Deserialize<GuiConfig>(File.ReadAllText(ConfigPath), JsonOptions);
                if (cfg != null)
                {
                    cfg.Normalize();
                    return cfg;
                }
            }
        }
        catch
        {
            // 配置损坏时回退默认值。
        }
        var fresh = new GuiConfig();
        fresh.Normalize();
        return fresh;
    }

    public void Normalize()
    {
        if (string.IsNullOrWhiteSpace(DataDir))
        {
            DataDir = DefaultDataDir;
        }
        if (string.IsNullOrWhiteSpace(CorePath))
        {
            CorePath = DefaultCorePath;
        }
        if (Port is < 1 or > 65535)
        {
            Port = 50011;
        }
        if (string.IsNullOrWhiteSpace(Profile))
        {
            Profile = "low";
        }
        if (string.IsNullOrWhiteSpace(LogLevel))
        {
            LogLevel = "info";
        }
    }

    public void Save()
    {
        Directory.CreateDirectory(MachineRoot);
        File.WriteAllText(ConfigPath, JsonSerializer.Serialize(this, JsonOptions));
    }
}
