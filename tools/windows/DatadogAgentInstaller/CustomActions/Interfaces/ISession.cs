using Microsoft.Deployment.WindowsInstaller;
using System.Collections.Generic;
using System.Diagnostics;
using System.Runtime.CompilerServices;

namespace Datadog.CustomActions.Interfaces
{
    public interface ISession
    {
        /// <summary>
        /// see <see cref="Session.this[string]"/>
        /// </summary>
        string this[string property] { get; set; }

        /// <summary>
        /// see <see cref="Session.Message"/>
        /// </summary>
        MessageResult Message(InstallMessage messageType, Record record);

        /// <summary>
        /// see <see cref="Session.Log(string)"/>
        /// </summary>
        void Log(
            string msg,
            [CallerMemberName] string memberName = null,
            [CallerFilePath] string filePath = null,
            [CallerLineNumber] int lineNumber = 0);

        /// <summary>
        /// see <see cref="Session.Components"/>
        /// </summary>
        ComponentInfoCollection Components { get; }

        /// <summary>
        /// see <see cref="Session.Features"/>
        /// </summary>
        IFeatureInfo Feature(string FeatureName);

        /// <summary>
        /// see <see cref="Session.CustomActionData"/>
        /// </summary>
        CustomActionData CustomActionData { get; }

        /// <summary>
        /// see <see cref="SessionWrapper.RunCommand"/>
        /// </summary>
        Process RunCommand(string filename, string arguments);

        /// <summary>
        /// see <see cref="SessionWrapper.RunCommand"/>
        /// </summary>
        Process RunCommand(string filename, string arguments, IDictionary<string, string> environment);
    }
}
