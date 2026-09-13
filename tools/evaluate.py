#!/usr/bin/env python3
"""Bounded diagnostic evaluator. Editable local files are NOT protected authority."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import shutil
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
REQUIRED = {
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureManagedImageContractPrecedesDeployment'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureImageMetadataRequiresMatchingIdentityAndCompleteShape'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureCreationRefusesOccupiedOrUnknownResourceNames'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureCreationCannotRetagForeignDisk'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureDiskTaggingRequiresOwnedCreationGraph'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureLostTaggingRecoversWithoutRedeployment'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestCreationRecoveryRequiresCheckpointAndNeverCreatesReplacement'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSEnvelopeRequiresCompleteInventory'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSRecordedVolumeSurvivesDiscoveryLoss'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSSDKBindsAccountAndEC2ToOneIdentity'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSProfileProjectionAndFrozenIdentity'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSProfileRejectsProcessAndAmbientSources'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSWebIdentityUsesProjectedTokenAndScopedEndpoint'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSSDKCreationReceiptAndOrderedCleanup'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSSDKLostCreateResponseRecoversDependencies'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSSDKAccountDriftStopsEffects'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSSDKDefinitiveCapacityRejectionHasNoReceipt'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestCreationReceiptPersistsDependenciesAndOverridesRetryClassification'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestCloudDependenciesPersistBeforeDeleteAndSurviveRestart'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestCloudDependencyCheckpointConflictPreventsDeletion'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestInvalidCloudDependenciesPreventDeletion'),
    ('github.com/tsouza/runnerscout/internal/state', 'TestDependencyStateCannotBeDowngradedOrDiscarded'),
    ('github.com/tsouza/runnerscout/internal/state', 'TestUnknownAllocationFieldsAreNotSilentlyLost'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestUnsupportedDependencyRecordsRefuseCloudEffects'),
    ('github.com/tsouza/runnerscout/cmd/runnerscout', 'TestInterruptedSafetyCheckCannotReportSuccessfulExit'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCompleteMulticloudExampleCompilesWithoutEnablingAdmissions'),
    ('github.com/tsouza/runnerscout/cmd/runnerscout', 'TestCRDChecksRequireOneExplicitMode'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCRDCheckValidatesSecretsWithoutStartingWorkers'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestUninstallCheckRequiresRootAbsenceAndIsReadOnly'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestUninstallCheckRefusesMissingCheckpointOrLifetimeRecords'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestUninstallCheckDetectsConcurrentRootRecreation'),
    ('github.com/tsouza/runnerscout/cmd/runnerscout', 'TestShutdownExitPreservesCleanupFailures'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeBindingChangeRecoversOriginalFleetAfterRestart'),
    ('github.com/tsouza/runnerscout/cmd/runnerscout', 'TestCRDCommandRejectsMixedConfigurationAndAuthentication'),
    ('github.com/tsouza/runnerscout/cmd/runnerscout', 'TestMountedValidationRemainsOfflineAndStrict'),
    ('github.com/tsouza/runnerscout/cmd/runnerscout', 'TestHealthListenerFailurePreventsControllerEffects'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeProtectionFailureRetainsCandidateCleanup'),
    ('github.com/tsouza/runnerscout/internal/operator', 'TestRecoveryWithoutGitHubPreservesAcceptedJobsAndDeadlines'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestLoadRechecksConfigurationAfterReadingSecrets'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestLoadedKeyIgnoresStatusButDetectsIdentityAndSecretRotation'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCleanupCredentialsDoNotRequireGitHubSecret'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCheckpointPreservesRecoverableBindingWithoutMetadataOrSecrets'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCheckpointRejectsForeignMalformedAndOversizedState'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCheckpointConflictAndRemovalKeepOwnershipPreconditions'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeProtectsBeforeSessionAndReloadsOnlySemanticChanges'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeCheckpointLossOrCorruptionPausesAndCannotBeReplaced'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeRecoversAcceptedConfigurationWithoutDrainingJobs'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeDeletionRequiresObservedDrainAndRetainsOtherFinalizers'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeFinalizerConflictRetriesWithoutRepeatingCloudCleanup'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeReplacementRetainsOldOwnershipWithoutCleanupLoop'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeRetriesCredentialCleanupAfterJoiningWorker'),
    ('github.com/tsouza/runnerscout/internal/operator', 'TestSuspensionPreservesAcceptedAdmissionsAndDeadlines'),
    ('github.com/tsouza/runnerscout/internal/operator', 'TestDrainRequiresObservedAbsenceAndCannotBeResumed'),
    ('github.com/tsouza/runnerscout/internal/operator', 'TestDrainRetiresPendingWorkAndRejectsMissingRecords'),
 ('github.com/tsouza/runnerscout/internal/operator', 'TestOperatorCanceledBeforeLeadershipStopsCleanly'),
 ('github.com/tsouza/runnerscout/internal/provider', 'TestProviderCredentialScopesSeparateNamedIdentities'),
 ('github.com/tsouza/runnerscout/internal/provider', 'TestProviderCredentialScopesRejectPartialAndCrossProviderSettings'),
 ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureNamedCredentialsUseExplicitSDKMechanisms'),
 ('github.com/tsouza/runnerscout/internal/operator', 'TestOperatorNamedCredentialsStayOutOfDurableState'),
 ('github.com/tsouza/runnerscout/internal/operator', 'TestOperatorCredentialFailureRollsBackScopes'),
 ('github.com/tsouza/runnerscout/internal/operator', 'TestOperatorShutdownWaitsForLeaderOperations'),
 ('github.com/tsouza/runnerscout/internal/configapi', 'TestResolveSecretsUsesNamedLocalKeysAndRedactsMaterial'),
 ('github.com/tsouza/runnerscout/internal/configapi', 'TestResolveSecretsRejectsRotationRecreationAndNamespaceSpoofing'),
 ('github.com/tsouza/runnerscout/internal/configapi', 'TestResolveSecretsRejectsMissingKeysAndInvalidEnvironment'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCompileAzureSDKCredentialsExcludeCLICaches'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureSDKPaginatedInventoryAndObservedCleanup'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureSDKAuthenticationAndTransportFailuresStayUnknown'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureSDKCreateTimeoutRetainsUnknownCommitment'),

    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeMissingRootCannotRemainReady'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeConditionRejectsUnobservedRootChanges'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeConditionWriteFailureClearsReadiness'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestRuntimeRejectsRootChangedDuringWorkerPreparation'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSAccountDriftCannotConfirmAbsence'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAWSCreateUsesDurableTokenAndPrivateBootstrap'),
    ('github.com/tsouza/runnerscout/internal/admission', 'TestAdmissionLimits'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureActiveDeploymentCannotProveAbsence'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureCreateUsesSecureBootstrapAndSpotDelete'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureForeignDiskCannotBeDeleted'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestAzureResidualOwnedNICRetainsCleanup'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestCapacityRejectionKeepsDeadline'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCompileDoesNotPretendUnsupportedExecutionExists'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCompileNetworkMappingsRequireIsolationAndCoverage'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCompilePreservesMulticloudConstraintsAndReferenceIsolation'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCompileRejectsBrokenOrCrossNamespaceReferences'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestCompileStaleCatalogAllowsRecoveryButNotAdmission'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestCooldownRetainsUnknownSearch'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestDeadlineInclusiveNoCloudEffect'),
    ('github.com/tsouza/runnerscout/internal/recovery', 'TestDisabledAndExactRESTIdentity'),
    ('github.com/tsouza/runnerscout/internal/operator', 'TestDurableAdmissionSurvivesOperatorRestart'),
    ('github.com/tsouza/runnerscout/internal/placement', 'TestFallbackRequiresDefinitiveExhaustion'),
    ('github.com/tsouza/runnerscout/internal/state', 'TestForeignStateRejected'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPOwnershipBlocksDeletion'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPSDKCreatePrivateSpotVMAndTaggedBootDisk'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPSDKLostResponseAndResidualDiskCleanup'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPSDKUnknownOperationsAndInventoryRetainOwnership'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPSDKCreateRefusesOccupiedDiskIdentity'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPSDKPendingCreateHonorsCancellation'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPCredentialFilesIsolateRealTokenExchanges'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPExplicitCredentialFailureNeverFallsBack'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestGCPCredentialExchangeCancellationAndRecovery'),

    ('github.com/tsouza/runnerscout/internal/placement', 'TestHardConstraintsAndFreshness'),
    ('github.com/tsouza/runnerscout/internal/placement', 'TestIndependentSmallCatalogOracle'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestIntentConflictPreventsCloudCall'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestJITPreparationFailureHasNoCloudEffects'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestJITPreparationFailureRetriesWithoutCloudCommitment'),
    ('github.com/tsouza/runnerscout/internal/operator', 'TestLimitUpgradeMigratesLegacyBindingButPreservesIdentity'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestLostCreateResponseRestartAndCleanup'),
    ('github.com/tsouza/runnerscout/internal/admission', 'TestLoweredLimitDrainsWithoutRearmingOrDroppingAdmissions'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestMissingInventoryCannotConfirmCleanup'),
    ('github.com/tsouza/runnerscout/internal/placement', 'TestOnDemandFallbackHonorsPriorOutcomes'),
    ('github.com/tsouza/runnerscout/internal/state', 'TestPersistenceAcrossStoreInstances'),
    ('github.com/tsouza/runnerscout/internal/operator', 'TestProviderBindingCannotChangeUnderExistingFleet'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestReadRejectsConcurrentMutationAndObjectRecreation'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestReadRejectsUnknownConfigurationAndNamespaceSpoofing'),
    ('github.com/tsouza/runnerscout/internal/configapi', 'TestReadUsesOnlyLocalNamedDependenciesAndRechecksVersions'),
    ('github.com/tsouza/runnerscout/internal/health', 'TestReadinessDoesNotTurnDependencyFailureIntoLivenessFailure'),
    ('github.com/tsouza/runnerscout/internal/operator', 'TestReadinessRequiresScaleSetSessionAndClearsOnExit'),
    ('github.com/tsouza/runnerscout/internal/admission', 'TestRedeliveryAndBoundedRearming'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestUnknownAbsenceNeverBlindlyCreates'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestUnknownCreateWithStartedJobSurvivesProvisioningDeadline'),
    ('github.com/tsouza/runnerscout/internal/provider', 'TestUnknownProviderOutputIsNotAbsence'),
    ('github.com/tsouza/runnerscout/internal/lifecycle', 'TestUnreadyVMExpiresButStartedJobDoesNot'),
}

def test_manifest(lines, required=REQUIRED):
 started=set();passed=set();failed=set();skipped=set()
 package_started=set();package_passed=set();package_skipped=set()
 for line in lines:
  event=json.loads(line)
  package=event.get('Package');name=event.get('Test');key=(package,name)
  action=event.get('Action')
  if action=='fail':failed.add(key)
  if not name and action=='start':package_started.add(package)
  if not name and action=='pass':package_passed.add(package)
  if name and action=='run':started.add(key)
  if name and action=='pass':passed.add(key)
  if name and action=='skip':skipped.add(key)
  if not name and action=='skip':package_skipped.add(package)
 missing=required-(started & passed)
 required_packages={package for package,test in required}
 relevant_packages=required_packages | {p for p,n in started}
 skipped.update((p,None) for p in package_skipped & relevant_packages)
 incomplete_packages=(package_started | relevant_packages)-package_passed-(package_skipped-relevant_packages)
 complete=bool(started) and not failed and not skipped and not missing and not incomplete_packages and started<=passed
 return dict(pass_=complete,started=len(started),completed=len(started & passed),failed=sorted(str(x) for x in failed),skipped=sorted(str(x) for x in skipped),missing=sorted(str(x) for x in missing),incomplete_packages=sorted(str(x) for x in incomplete_packages))

def digest():
 h=hashlib.sha256()
 for p in sorted(ROOT.rglob('*')):
  if not p.is_file() or any(part in {'.git','bin','evidence','work','__pycache__'} for part in p.relative_to(ROOT).parts):continue
  h.update(str(p.relative_to(ROOT)).encode()+b'\0'+p.read_bytes()+b'\0')
 return h.hexdigest()

def main():
 os.chdir(ROOT);evidence=ROOT/'evidence';evidence.mkdir(exist_ok=True)
 # flock serializes local evaluation and budget accounting. The maintainer can
 # edit these files; this protects concurrent invocations, not against the candidate.
 import fcntl
 with (evidence/'evaluation.lock').open('a') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX)
  ledger=evidence/'attempts.jsonl';history=[json.loads(x) for x in ledger.read_text().splitlines()] if ledger.exists() else []
  attempts=sum(x['event']=='start' for x in history)
  consumed=sum(x.get('seconds',0) for x in history if x['event']=='finish')
  if shutil.disk_usage(ROOT).free < 2*1024**3:raise SystemExit('at least 2 GiB free disk required for evaluation')
  if attempts>=60 or consumed>=21600:raise SystemExit('development evaluation budget exhausted')
  run=evidence/str(uuid.uuid4());run.mkdir();start=time.monotonic();identity=digest()
  def append(record):
   with ledger.open('a') as f:f.write(json.dumps(record,sort_keys=True)+'\n');f.flush();os.fsync(f.fileno())
  append(dict(event='start',run=run.name,candidate=identity,time=time.time()))
  report=dict(baseline='DEV-0001',candidate_sha256=identity,evaluator_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),protected=False,live_cloud_integration=False,commands=[])
  deadline=start+min(600,21600-consumed)
  def command(args,name):
   remaining=deadline-time.monotonic()
   if remaining<=0:raise TimeoutError('evaluation deadline exhausted')
   with (run/name).open('w') as log:
    p=subprocess.run(args,stdout=log,stderr=subprocess.STDOUT,timeout=remaining,check=False)
   report['commands'].append(dict(argv=args,exit_code=p.returncode,log=name))
   if p.returncode:raise RuntimeError(f'{name} failed (exit {p.returncode})')
   return (run/name).read_text()
  try:
   command([sys.executable,'tools/source_hygiene.py'],'source-hygiene.log')
   formatting=command(['gofmt','-l','cmd','internal','api'],'format.log')
   if formatting.strip():raise RuntimeError('unformatted Go files')
   command([sys.executable,'-m','unittest','discover','-s','tools','-p','test_*.py'],'harness.log')
   command(['go','vet','./...'],'vet.log')
   raw=command(['go','test','-json','-race','-count=1','./...'],'tests.jsonl')
   report['tests']=test_manifest(raw.splitlines())
   if not report['tests']['pass_']:raise RuntimeError('missing, skipped, failed or incomplete required tests')
   command(['go','build','-trimpath','-o','bin/runnerscout','./cmd/runnerscout'],'build.log')
   if digest()!=identity:raise RuntimeError('candidate changed during evaluation')
   report['verdict']='pass'
  except Exception as e:
   report['verdict']='inconclusive' if isinstance(e,(TimeoutError,subprocess.TimeoutExpired)) else 'fail'
   report['reason']=str(e)
  finally:
   report['seconds']=time.monotonic()-start
   (run/'manifest.json').write_text(json.dumps(report,indent=2)+'\n')
   append(dict(event='finish',run=run.name,seconds=report['seconds'],verdict=report['verdict']))
  print(json.dumps(dict(verdict=report['verdict'],evidence=str(run.relative_to(ROOT)),tests=report.get('tests'),reason=report.get('reason'))))
  return 0 if report['verdict']=='pass' else 1

if __name__=='__main__':sys.exit(main())
