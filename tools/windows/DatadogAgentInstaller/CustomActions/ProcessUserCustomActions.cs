using Datadog.CustomActions.Extensions;
using Datadog.CustomActions.Interfaces;
using Datadog.CustomActions.Native;
using Microsoft.Deployment.WindowsInstaller;
using System;
using System.DirectoryServices.ActiveDirectory;
using System.Security.Cryptography;
using System.Security.Principal;
using System.Windows.Forms;

namespace Datadog.CustomActions
{
    // InvalidAgentUserException is a custom exception type that contains a message that can be displayed to the user in a dialog box.
    // Use to distinguish an expected user configuration error with a message that should be displayed to the user from an unexpected runtime exception.
    // This message is displayed to the customer in a dialog box. Ensure the text is well formatted.
    public class InvalidAgentUserConfigurationException : Exception
    {
        public InvalidAgentUserConfigurationException()
        {
        }

        public InvalidAgentUserConfigurationException(string message) : base(message)
        {
        }

        public InvalidAgentUserConfigurationException(string message, Exception inner) : base(message, inner)
        {
        }
    }

    public class ProcessUserCustomActions
    {
        private readonly ISession _session;
        private readonly INativeMethods _nativeMethods;
        private readonly IServiceController _serviceController;
        private readonly IRegistryServices _registryServices;

        private bool _isDomainController;
        private bool _isReadOnlyDomainController;

        public ProcessUserCustomActions(
            ISession session,
            INativeMethods nativeMethods,
            IServiceController serviceController,
            IRegistryServices registryServices)
        {
            _session = session;
            _nativeMethods = nativeMethods;
            _serviceController = serviceController;
            _registryServices = registryServices;

            // Get domain controller status once and cache the result.
            // This call can make network requests so it's better to do it once.
            // Plus it's used in multiple places and this lets us avoid passing it around as a bool parameter.
            FetchDomainControllerStatus();
        }

        public ProcessUserCustomActions(ISession session)
            : this(
                session,
                new Win32NativeMethods(),
                new ServiceController(),
                new RegistryServices()
            )
        {
        }

        private static string GetRandomPassword(int length)
        {
            var rgb = new byte[length];
            var rngCrypt = new RNGCryptoServiceProvider();
            rngCrypt.GetBytes(rgb);
            return Convert.ToBase64String(rgb);
        }

        /// <summary>
        /// Determine the default 'domain' part of a user account name when one is not provided by the user.
        /// </summary>
        ///
        /// <remarks>
        /// We default to creating a local account if the domain
        /// part is not specified in DDAGENTUSER_NAME.
        /// However, domain controllers do not have local accounts, so we must
        /// default to a domain account.
        /// We still want to default to local accounts for domain clients
        /// though, so it is not enough to check if the computer is domain joined,
        /// we must specifically check if this computer is a domain controller.
        /// </remarks>
        private string GetDefaultDomainPart()
        {
            if (!_isDomainController)
            {
                return Environment.MachineName;
            }

            try
            {
                return _nativeMethods.GetComputerDomain();
            }
            catch (ActiveDirectoryObjectNotFoundException)
            {
                // Computer is not joined to a domain, it can't be a DC
            }

            // Computer is not a DC, default to machine name (NetBIOS name)
            return Environment.MachineName;
        }

        /// <summary>
        /// Returns true if we will treat <c>name</c> as an alias for the local machine name.
        /// </summary>
        /// <remarks>
        /// Comparisons are case-insensitive.
        /// </remarks>
        private bool NameIsLocalMachine(string name)
        {
            name = name.ToLower();
            if (name == Environment.MachineName.ToLower())
            {
                return true;
            }

            // Windows runas does not support the following names, but we can try to.
            if (name == ".")
            {
                return true;
            }

            // Windows runas and logon screen do not support the following names, but we can try to.
            if (_nativeMethods.GetComputerName(COMPUTER_NAME_FORMAT.ComputerNameDnsHostname, out var hostname))
            {
                if (name == hostname.ToLower())
                {
                    return true;
                }
            }

            if (_nativeMethods.GetComputerName(COMPUTER_NAME_FORMAT.ComputerNameDnsFullyQualified, out var fqdn))
            {
                if (name == fqdn.ToLower())
                {
                    return true;
                }
            }

            return false;
        }

        /// <summary>
        /// Returns true if <c>name</c> should be replaced by GetDefaultDomainPart()
        /// </summary>
        /// <remarks>
        /// Comparisons are case-insensitive.
        /// </remarks>
        private bool NameUsesDefaultPart(string name)
        {
            if (name == ".")
            {
                return true;
            }

            return false;
        }

        /// <summary>
        /// Gets the domain and user parts from an account name.
        /// </summary>
        /// <remarks>
        /// Windows has varying support for name syntax accross the OS, this function may normalize the domain
        /// part to the machine name (NetBIOS name) or the domain name.
        /// See NameUsesDefaultPart and NameIsLocalMachine for details on the supported aliases.
        /// For example,
        ///   * on regular hosts, .\user => machinename\user
        ///   * on domain controllers, .\user => domain\user
        /// </remarks>
        private void ParseUserName(string account, out string userName, out string domain)
        {
            // We do not use CredUIParseUserName because it does not handle some cases nicely.
            // e.g. CredUIParseUserName(host.ddev.net\user) returns userName=.ddev.net domain=host.ddev.net
            // e.g. CredUIParseUserName(.\user) returns userName=.\user domain=
            if (account.Contains("\\"))
            {
                var parts = account.Split('\\');
                domain = parts[0];
                userName = parts[1];
                if (NameUsesDefaultPart(domain))
                {
                    domain = GetDefaultDomainPart();
                }
                else if (NameIsLocalMachine(domain))
                {
                    domain = Environment.MachineName;
                }

                return;
            }

            // If no \\, then full string is username
            userName = account;
            domain = "";
        }

        /// <summary>
        /// Wrapper for the LookupAccountName Windows API that also supports additional syntax for the domain part of the name.
        /// See ParseUserName for details on the supported names.
        /// </summary>
        private bool LookupAccountWithExtendedDomainSyntax(
            string account,
            out string userName,
            out string domain,
            out SecurityIdentifier securityIdentifier,
            out SID_NAME_USE nameUse)
        {
            // Provide the account name to Windows as is first, see if Windows can handle it.
            var userFound = _nativeMethods.LookupAccountName(account,
                out userName,
                out domain,
                out securityIdentifier,
                out nameUse);
            if (!userFound)
            {
                // The first LookupAccountName failed, this could be because the user does not exist,
                // or it could be because the domain part of the name is invalid.
                ParseUserName(account, out var tmpUser, out var tmpDomain);
                // Try LookupAccountName again but using a fixed domain part.
                account = $"{tmpDomain}\\{tmpUser}";
                _session.Log($"User not found, trying again with fixed domain part: {account}");
                userFound = _nativeMethods.LookupAccountName(account,
                    out userName,
                    out domain,
                    out securityIdentifier,
                    out nameUse);
            }

            return userFound;
        }


        /// <summary>
        /// Throws an exception if the agent user is the same as the current user.
        /// </summary>
        /// <remarks>
        /// Since the installer modifies the user account, if the current user is provided for the ddagentuser the account will be locked out.
        /// If a customer does this by mistake, they will have to use a different account to log in to the machine and fix the account.
        /// To avoid this, we disallow using the current user as the ddagentuser unless the user is a service account (e.g. LocalSystem)
        /// </remarks>
        private void TestAgentUserIsNotCurrentUser(SecurityIdentifier agentUser, bool isServiceAccount)
        {
            string currentUserName;
            SecurityIdentifier currentUserSID;
            try
            {
                _nativeMethods.GetCurrentUser(out currentUserName, out currentUserSID);
            }
            catch (Exception e)
            {
                _session.Log($"Unable to get current user SID: {e}");
                return;
            }
            _session.Log($"Currently logged in user: {currentUserName} ({currentUserSID})");

            // If the user is a service account (e.g. LocalSystem) then it's ok to use the same account
            if (isServiceAccount)
            {
                return;
            }

            // good, agent user and current user are different
            if (!currentUserSID.Equals(agentUser))
            {
                return;
            }

            throw new InvalidAgentUserConfigurationException("The account provided is the same as the currently logged in user. Please supply a different account for the Datadog Agent.");
        }

        /// <summary>
        /// Throws an exception if the password is required but not provided.
        /// </summary>
        private void TestIfPasswordIsRequiredAndProvidedForExistingAccount(string ddAgentUserName, string ddAgentUserPassword,
            bool isServiceAccount, bool isDomainAccount, bool datadogAgentServiceExists)
        {
            var passwordProvided = !string.IsNullOrEmpty(ddAgentUserPassword);

            // If password is provided or the account is a service account (no password), we're good
            if (passwordProvided || isServiceAccount)
            {
                return;
            }

            // If the service already exists (like during upgrade) then we don't need a password
            if (datadogAgentServiceExists)
            {
                return;
            }

            // If the account name looks like a gMSA account, but wasn't detected as one.
            // Only look for $ at the end of the account name if it's a domain account, because
            // normal account names can end with $. In the case of a domain account that ends
            // in $ that is NOT intended to be a gMSA account, the user must provide a password.
            if (_isDomainController || isDomainAccount)
            {
                if (ddAgentUserName.EndsWith("$") && !isServiceAccount)
                {
                    throw new InvalidAgentUserConfigurationException(
                        $"The provided account '{ddAgentUserName}' ends with '$' but is not recognized as a valid gMSA account. Please ensure the username is correct and this host is a member of PrincipalsAllowedToRetrieveManagedPassword. If the account is a normal account, please provide a password.");
                }
            }

            if (_isDomainController)
            {
                // We choose not to create/manage the account/password on domain controllers because
                // the account can be replicated/used across the domain/forest.
                throw new InvalidAgentUserConfigurationException(
                    "A password was not provided. Passwords are required for non-service accounts on Domain Controllers.");
            }

            if (isDomainAccount)
            {
                // We can't create a new account or change a password from a domain client, so we must require a password
                throw new InvalidAgentUserConfigurationException(
                    "A password was not provided. Passwords are required for domain accounts.");
            }
        }

        private ActionResult HandleProcessDdAgentUserCredentialsException(Exception e, string errorDialogMessage, bool calledFromUIControl)
        {
            _session.Log($"Error processing ddAgentUser credentials: {e}");
            if (calledFromUIControl)
            {
                // When called from a UI control we must store the error information in the session
                // because logging is not available.
                _session["ErrorModal_ExceptionInformation"] = e.ToString();
                // When called from InstallUISequence we must return success for the modal dialog to show,
                // otherwise the installer exits. The control that called this action should check the
                // DDAgentUser_Valid property to determine if this function succeeded or failed.
                // Error information is contained in the ErrorModal_ErrorMessage property.
                // MsiProcessMessage doesn't work here so we must use our own custom error popup.
                _session["ErrorModal_ErrorMessage"] = errorDialogMessage;
                _session["DDAgentUser_Valid"] = "False";
                return ActionResult.Success;
            }

            // Send an error message, the installer may display an error popup depending on the UILevel.
            // https://learn.microsoft.com/en-us/windows/win32/msi/user-interface-levels
            {
                using var actionRecord = new Record
                {
                    FormatString = errorDialogMessage
                };
                _session.Message(InstallMessage.Error
                                 | (InstallMessage)((int)MessageBoxButtons.OK | (int)MessageBoxIcon.Warning),
                    actionRecord);
            }
            // When called from InstallExecuteSequence we want to fail on error
            return ActionResult.Failure;
        }

        /// <summary>
        /// Returns the Agent user password from the command line, or the LSA secret store
        /// </summary>
        private string FetchAgentPassword()
        {
            var ddAgentUserPassword = _session.Property("DDAGENTUSER_PASSWORD");
            if (!string.IsNullOrEmpty(ddAgentUserPassword))
            {
                return ddAgentUserPassword;
            }

            // If the password is not provided on the command line, try to fetch it from the LSA secret store
            try
            {
                var keyName = ConfigureUserCustomActions.AgentPasswordPrivateDataKey();
                ddAgentUserPassword = _nativeMethods.FetchSecret(keyName);
            }
            catch (Exception e)
            {
                // Ignore errors, the password may not exist yet
                _session.Log($"Failed to read Agent password from LSA, using empty string, this is unexpected only during upgrades: {e}");
            }

            return ddAgentUserPassword;
        }

        /// <summary>
        /// Processes the DDAGENTUSER_NAME and DDAGENTUSER_PASSWORD properties into formats that can be
        /// consumed by other custom actions. Also does some basic error handling/checking on the property values.
        /// </summary>
        /// <param name="calledFromUIControl"></param>
        /// <returns></returns>
        /// <remarks>
        /// This function must support being called multiple times during the install, as the user can back/next the
        /// UI multiple times.
        ///
        /// When calledFromUIControl is true: sets property DDAgentUser_Valid="True" on success, on error, stores error information in the ErrorModal_ErrorMessage property.
        ///
        /// When calledFromUIControl is false (during InstallExecuteSequence), sends an InstallMessage.Error message.
        /// The installer may display an error popup depending on the UILevel.
        /// https://learn.microsoft.com/en-us/windows/win32/msi/user-interface-levels
        /// </remarks>
        public ActionResult ProcessDdAgentUserCredentials(bool calledFromUIControl = false)
        {
            try
            {
                if (calledFromUIControl)
                {
                    // reset output properties
                    _session["ErrorModal_ErrorMessage"] = "";
                    _session["DDAgentUser_Valid"] = "False";
                }

                var ddAgentUserName = _session.Property("DDAGENTUSER_NAME");
                var ddAgentUserPassword = FetchAgentPassword();
                var datadogAgentServiceExists = _serviceController.ServiceExists(Constants.AgentServiceName);

                // LocalSystem is not supported by LookupAccountName as it is a pseudo account,
                // do the conversion here for user's convenience.
                if (ddAgentUserName == "LocalSystem")
                {
                    ddAgentUserName = "NT AUTHORITY\\SYSTEM";
                }
                else if (ddAgentUserName == "LocalService")
                {
                    ddAgentUserName = "NT AUTHORITY\\LOCAL SERVICE";
                }
                else if (ddAgentUserName == "NetworkService")
                {
                    ddAgentUserName = "NT AUTHORITY\\NETWORK SERVICE";
                }

                if (string.IsNullOrEmpty(ddAgentUserName))
                {
                    if (_isDomainController)
                    {
                        // require user to provide a username on domain controllers so that the customer is explicit
                        // about the username/password that will be created on their domain if it does not exist.
                        throw new InvalidAgentUserConfigurationException("A username was not provided. A username is a required when installing on Domain Controllers.");
                    }

                    // Creds are not in registry and user did not pass a value, use default account name
                    ddAgentUserName = $"{GetDefaultDomainPart()}\\ddagentuser";
                    _session.Log($"No creds provided, using default {ddAgentUserName}");
                }

                // Check if user exists, and parse the full account name
                var userFound = LookupAccountWithExtendedDomainSyntax(
                    ddAgentUserName,
                    out var userName,
                    out var domain,
                    out var securityIdentifier,
                    out var nameUse);
                var isServiceAccount = false;
                var isDomainAccount = false;
                if (userFound)
                {
                    _session.Log($"Found {userName} in {domain} as {nameUse}");
                    // Ensure name belongs to a user account or special accounts like SYSTEM, and not to a domain, computer or group.
                    if (nameUse != SID_NAME_USE.SidTypeUser && nameUse != SID_NAME_USE.SidTypeWellKnownGroup)
                    {
                        throw new InvalidAgentUserConfigurationException("The name provided is not a user account. Please supply a user account name in the format domain\\username.");
                    }

                    _session["DDAGENTUSER_FOUND"] = "true";
                    _session["DDAGENTUSER_SID"] = securityIdentifier.ToString();
                    isServiceAccount = _nativeMethods.IsServiceAccount(securityIdentifier);
                    if (isServiceAccount)
                    {
                        _session["DDAGENTUSER_IS_SERVICE_ACCOUNT"] = "true";
                    }
                    else
                    {
                        _session["DDAGENTUSER_IS_SERVICE_ACCOUNT"] = "false";
                    }
                    isDomainAccount = _nativeMethods.IsDomainAccount(securityIdentifier);
                    _session.Log(
                        $"\"{domain}\\{userName}\" ({securityIdentifier.Value}, {nameUse}) is a {(isDomainAccount ? "domain" : "local")} {(isServiceAccount ? "service " : string.Empty)}account");

                    TestAgentUserIsNotCurrentUser(securityIdentifier, isServiceAccount);
                    TestIfPasswordIsRequiredAndProvidedForExistingAccount(userName, ddAgentUserPassword, isServiceAccount, isDomainAccount, datadogAgentServiceExists);
                }
                else
                {
                    _session["DDAGENTUSER_FOUND"] = "false";
                    _session["DDAGENTUSER_SID"] = null;
                    _session.Log($"User {ddAgentUserName} doesn't exist.");

                    ParseUserName(ddAgentUserName, out userName, out domain);
                    if (_isDomainController || _isReadOnlyDomainController)
                    {
                        // user must be domain account on DCs
                        isDomainAccount = true;
                    }
                }

                if (string.IsNullOrEmpty(userName))
                {
                    // If userName is empty at this point, then it is likely that the input is malformed
                    throw new InvalidAgentUserConfigurationException($"Unable to parse account name from {ddAgentUserName}. Please ensure the account name follows the format domain\\username.");
                }

                if (string.IsNullOrEmpty(domain))
                {
                    // This case is hit if user specifies a username without a domain part and it does not exist
                    _session.Log("domain part is empty, using default");
                    domain = GetDefaultDomainPart();
                }

                // User does not exist and we cannot create user account from RODC
                if (!userFound && _isReadOnlyDomainController)
                {
                    throw new InvalidAgentUserConfigurationException("The account does not exist. Domain accounts must already exist when installing on Read-Only Domain Controllers.");
                }

                // We are trying to create a user in a domain on a non-domain controller.
                // This must run *after* checking that the domain is not empty.
                if (!userFound &&
                    !_isDomainController &&
                    domain != Environment.MachineName)
                {
                    throw new InvalidAgentUserConfigurationException("The account does not exist. Domain accounts must already exist when installing on Domain Clients.");
                }

                _session.Log(
                    $"Installing with DDAGENTUSER_PROCESSED_NAME={userName} and DDAGENTUSER_PROCESSED_DOMAIN={domain}");
                // Create new DDAGENTUSER_PROCESSED_NAME property so we don't modify the property containing
                // the user provided value DDAGENTUSER_NAME
                _session["DDAGENTUSER_PROCESSED_NAME"] = userName;
                _session["DDAGENTUSER_PROCESSED_DOMAIN"] = domain;
                _session["DDAGENTUSER_PROCESSED_FQ_NAME"] = $"{domain}\\{userName}";

                _session["DDAGENTUSER_RESET_PASSWORD"] = null;
                if (!userFound &&
                    _isDomainController &&
                    string.IsNullOrEmpty(ddAgentUserPassword))
                {
                    // require user to provide a password on domain controllers so that the customer is explicit
                    // about the username/password that will be created on their domain if it does not exist.
                    throw new InvalidAgentUserConfigurationException("A password was not provided. A password is a required when installing on Domain Controllers.");
                }

                var isLocalAccount = !isServiceAccount && !isDomainAccount;
                if (isLocalAccount)
                {
                    if (string.IsNullOrEmpty(ddAgentUserPassword))
                    {
                        _session.Log("Generating a random password");
                        ddAgentUserPassword = GetRandomPassword(128);
                    }
                    // For local accounts, we will set the Agent account password to this value.
                    // This allows customers to change the password of the Agent account using the installer,
                    // without having to separately manually change it.
                    // It also ensures that the password we fetch from the LSA secret store won't
                    // be the wrong password (older installers may have reset the password to a different random password).
                    _session["DDAGENTUSER_RESET_PASSWORD"] = "yes";
                }
                else if (isServiceAccount && !string.IsNullOrEmpty(ddAgentUserPassword))
                {
                    _session.Log("Ignoring provided password because account is a service account");
                    ddAgentUserPassword = null;
                }

                if (!string.IsNullOrEmpty(ddAgentUserPassword))
                {
                    TestValidAgentUserPassword(ddAgentUserPassword);
                }

                _session["DDAGENTUSER_PROCESSED_PASSWORD"] = ddAgentUserPassword;
            }
            catch (InvalidAgentUserConfigurationException e)
            {
                return HandleProcessDdAgentUserCredentialsException(e, e.Message, calledFromUIControl);
            }
            catch (Exception e)
            {
                return HandleProcessDdAgentUserCredentialsException(e, "An unexpected error occurred while parsing the account name. Refer to the installation log for more information or contact support for assistance.", calledFromUIControl);
            }

            if (calledFromUIControl)
            {
                _session["DDAgentUser_Valid"] = "True";
            }

            return ActionResult.Success;
        }

        /// <summary>
        /// Fetches domain controller status. On error, logs error and sets isDomainController to false.
        /// </summary>
        private void FetchDomainControllerStatus()
        {
            // We check for errors here rather than in _nativeMethods just so we can log the error.
            try
            {
                _isDomainController = _nativeMethods.IsDomainController();
            }
            catch (Exception e)
            {
                // The underlying NetGetServerInfo call can fail if the Server service is not running or not available.
                // Since the Server service must be running on a DC, we can assume that this host is not a DC.
                // https://learn.microsoft.com/en-us/windows/win32/api/lmserver/nf-lmserver-netservergetinfo
                // If the host is actually a DC AND the user provides a domain account that does not exist,
                // then ProcessDdAgentUserCredentials will fail.
                _session.Log($"Error determining if this host is a domain controller, continuing assuming machine is a workstation/client: {e}");
                _session.Log("If this host is actually a DC, ensure the lanmanserver/Server service is running or provide an existing user account for DDAGENTUSER_NAME.");
                _isDomainController = false;
            }

            if (!_isDomainController)
            {
                _isReadOnlyDomainController = false;
                return;
            }

            // Host is a domain controller, fetch additional info
            try
            {
                _isReadOnlyDomainController = _nativeMethods.IsReadOnlyDomainController();
            }
            catch (Exception e)
            {
                // On error assume the DC is not read-only.
                // If the DC is actually read-only AND the user provides a domain account that does not exist,
                // then the installer will fail later when trying to create the account.
                _session.Log($"Error determining if this DC is read-only, continuing assuming it is not: {e}");
                _isReadOnlyDomainController = false;
            }
        }

        private void TestValidAgentUserPassword(string ddAgentUserPassword)
        {
            // password cannot contain semicolon
            // semicolon is the delimiter for CustomActionData, and we don't have special handling for this.
            // TODO: WINA-1226
            if (ddAgentUserPassword.Contains(";"))
            {
                throw new InvalidAgentUserConfigurationException("The password provided contains an invalid character. Please provide a password that does not contain a semicolon.");
            }
        }

        public static ActionResult ProcessDdAgentUserCredentials(Session session)
        {
            return new ProcessUserCustomActions(new SessionWrapper(session)).ProcessDdAgentUserCredentials(
                calledFromUIControl: false);
        }

        public static ActionResult ProcessDdAgentUserCredentialsUI(Session session)
        {
            return new ProcessUserCustomActions(new SessionWrapper(session)).ProcessDdAgentUserCredentials(
                calledFromUIControl: true);
        }
    }
}
